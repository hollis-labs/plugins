package pgplugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	"github.com/jackc/pgx/v5"
)

func TestAdoptionValidationAndOldLaneRefusal(t *testing.T) {
	valid := AdoptionMapping{Database: "existing_ledger", Schema: "public", LegacyOwner: "legacy", Tables: []TableAccess{{"calls", "read_write"}, {"history", "read"}}}
	if err := validateAdoption(&valid); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*AdoptionMapping){func(a *AdoptionMapping) { a.Database = "postgres" }, func(a *AdoptionMapping) { a.Schema = "pg_catalog" }, func(a *AdoptionMapping) { a.LegacyOwner = "name; SQL" }, func(a *AdoptionMapping) { a.Tables = []TableAccess{{"calls", "all"}} }, func(a *AdoptionMapping) { a.Tables = []TableAccess{{"calls", "read"}, {"calls", "read"}} }} {
		a := valid
		edit(&a)
		if validateAdoption(&a) == nil {
			t.Fatal("invalid adoption accepted")
		}
	}
	r := LifecycleRequest{AppRequest: AppRequest{Cluster: "scratch", App: "app", Environment: "dev"}, Adoption: &valid, IssuanceID: strings.Repeat("1", 32), DryRun: true}
	if validateLifecycle("provision_app", r) == nil {
		t.Fatal("provision accepted adoption override")
	}
	if err := validateLifecycle("adopt_app", r); err != nil {
		t.Fatal(err)
	}
	p := &Plugin{config: Config{Cluster: "scratch"}, backend: &postgresBackend{}}
	raw, _ := json.Marshal(r)
	var args map[string]any
	_ = json.Unmarshal(raw, &args)
	delete(args, "vector") // not an adoption input
	args["sql"] = "SELECT 1"
	if _, err := p.call(t.Context(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_adopt_app", Arguments: args}); err == nil {
		t.Fatal("SQL override accepted")
	}
}

func TestScratchAdoption(t *testing.T) {
	socket := os.Getenv("CERBERUS_PG_SCRATCH_SOCKET")
	if socket == "" {
		t.Skip("scratch fixture not supplied")
	}
	if !filepath.IsAbs(socket) || !strings.HasPrefix(filepath.Base(socket), "cerberus-pg-plugin-test.") {
		t.Fatal("refusing non-scratch endpoint")
	}
	ctx := context.Background()
	cfg := Config{Cluster: "scratch", Host: socket, Port: "5432", User: "postgres", Database: "postgres", Password: "fixture-admin-trust", AdminUser: "postgres", AdminPassword: "fixture-admin-trust"}
	connect := func(user, password, db, consumer string) *pgx.Conn {
		t.Helper()
		cc := cfg
		cc.User = user
		cc.Password = password
		cc.Database = db
		pc, e := connectionConfig(cc)
		if e != nil {
			t.Fatal(e)
		}
		pc.RuntimeParams["application_name"] = consumer
		c, e := pgx.ConnectConfig(ctx, pc)
		if e != nil {
			t.Fatalf("fixture connection failed for %s/%s", user, db)
		}
		t.Cleanup(func() { closeConn(c) })
		return c
	}
	admin := connect("postgres", cfg.Password, "postgres", "adoption_fixture")
	if e := secureWriter(ctx, admin); e != nil {
		t.Fatal(e)
	}
	exec := func(c *pgx.Conn, sql string) {
		t.Helper()
		if _, e := c.Exec(ctx, sql); e != nil {
			t.Fatalf("fixture statement failed: %s", sql)
		}
	}
	deny := func(c *pgx.Conn, sql string) {
		t.Helper()
		if _, e := c.Exec(ctx, sql); e == nil {
			t.Fatalf("forbidden statement succeeded: %s", sql)
		}
	}
	passwords := map[string]string{"reader": strings.Repeat("C", 43), "legacy": strings.Repeat("L", 43), "migration_password": strings.Repeat("M", 43), "runtime_password": strings.Repeat("R", 43), "password": strings.Repeat("N", 43)}
	canaries := []string{}
	for _, role := range []struct{ name, slot string }{{"adoption_reader", "reader"}, {"adoption_legacy", "legacy"}} {
		verifier, e := scramVerifier(passwords[role.slot])
		if e != nil {
			t.Fatal(e)
		}
		if _, e = admin.Exec(ctx, "CREATE ROLE "+ident(role.name)+" LOGIN PASSWORD "+literal(verifier)); e != nil {
			t.Fatal("fixture role failed")
		}
		canaries = append(canaries, verifier)
	}
	exec(admin, `GRANT pg_read_all_stats TO adoption_reader`)
	exec(admin, `GRANT EXECUTE ON FUNCTION pg_control_system() TO adoption_reader`)
	// Preserve the shared fixture's initial PUBLIC CONNECT state for the
	// read-only catalog test; this test owns only its named app fixtures.
	for _, db := range []string{"postgres", "template1"} {
		var allowed bool
		if admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database d CROSS JOIN LATERAL aclexplode(coalesce(datacl,acldefault('d',datdba))) a WHERE datname=$1 AND a.grantee=0 AND a.privilege_type='CONNECT')`, db).Scan(&allowed) != nil {
			t.Fatal("fixture ACL read failed")
		}
		if allowed {
			t.Cleanup(func() { _, _ = admin.Exec(ctx, "GRANT CONNECT ON DATABASE "+ident(db)+" TO PUBLIC") })
		}
	}
	exec(admin, `REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`)
	exec(admin, `REVOKE CONNECT ON DATABASE template1 FROM PUBLIC`)
	exec(admin, `GRANT CONNECT ON DATABASE postgres TO adoption_reader`)
	for _, db := range []string{"existing_ledger", "legacy_test"} {
		exec(admin, "CREATE DATABASE "+ident(db)+" OWNER adoption_legacy TEMPLATE template0")
		exec(admin, "REVOKE CONNECT ON DATABASE "+ident(db)+" FROM PUBLIC")
	}
	exec(admin, `GRANT CONNECT ON DATABASE existing_ledger TO adoption_reader`)
	appAdmin := connect("postgres", cfg.Password, "existing_ledger", "fixture_app_admin")
	if e := secureWriter(ctx, appAdmin); e != nil {
		t.Fatal(e)
	}
	legacy := connect("adoption_legacy", passwords["legacy"], "existing_ledger", "old_consumer")
	legacyTest := connect("adoption_legacy", passwords["legacy"], "legacy_test", "test_consumer")
	exec(legacy, `CREATE TABLE public.calls(id text PRIMARY KEY, detail jsonb NOT NULL, conversation_id text NOT NULL DEFAULT '')`)
	exec(legacy, `CREATE INDEX calls_detail ON public.calls(conversation_id)`)
	exec(legacy, `CREATE TABLE public.history(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`)
	exec(legacy, `INSERT INTO public.calls VALUES('before','{"secret":"synthetic-row-canary"}','')`)
	exec(legacy, `INSERT INTO public.history(version) VALUES(1),(2),(3)`)
	exec(legacyTest, `CREATE TABLE public.preserved(id int)`)
	fingerprint := func() string {
		t.Helper()
		var s string
		if e := appAdmin.QueryRow(ctx, `SELECT md5((SELECT string_agg(row(id,detail,conversation_id)::text,'|' ORDER BY id) FROM public.calls)||'/'||(SELECT string_agg(version::text,',' ORDER BY version) FROM public.history))`).Scan(&s); e != nil {
			t.Fatal("fixture fingerprint failed")
		}
		return s
	}
	before := fingerprint()
	var legacyVerifier string
	if admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname='adoption_legacy'`).Scan(&legacyVerifier) != nil {
		t.Fatal("fixture verifier unavailable")
	}
	bcfg := cfg
	bcfg.User = "adoption_reader"
	bcfg.Password = passwords["reader"]
	b := &postgresBackend{config: bcfg}
	a := &AdoptionMapping{Database: "existing_ledger", Schema: "public", LegacyOwner: "adoption_legacy", Tables: []TableAccess{{"calls", "read_write"}, {"history", "read"}}}
	r := LifecycleRequest{AppRequest: AppRequest{Cluster: "scratch", App: "adopted", Environment: "test"}, Adoption: a, IssuanceID: strings.Repeat("a", 32), DryRun: true}
	preview := func(op string, r LifecycleRequest) LifecyclePlan {
		t.Helper()
		p, e := b.Preview(ctx, op, r)
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	assertReady := func(p LifecyclePlan) {
		t.Helper()
		if !p.Executable {
			t.Fatalf("fixture blocked: %v", p.Blockers)
		}
	}
	issuance := func(op string, r LifecycleRequest, values map[string]string) cerbplugin.Issuance {
		refs := map[string]string{}
		for k := range values {
			refs[k] = "file:///fixture/" + r.IssuanceID + "-" + k
		}
		return cerbplugin.Issuance{ID: r.IssuanceID, Operation: op, BindingDigest: "fixture", References: refs}
	}
	execute := func(op string, r LifecycleRequest, p LifecyclePlan, values map[string]string) (LifecycleResult, error) {
		r.DryRun = false
		r.Acknowledged = true
		r.ExpectedPlan = p.PlanDigest
		return b.Execute(ctx, op, r, issuance(op, r, values), values)
	}
	p := preview("adopt_app", r)
	assertReady(p)
	serialized, _ := json.Marshal(p)
	if strings.Contains(string(serialized), "synthetic-row-canary") {
		t.Fatal("row data escaped preview")
	}
	if before != fingerprint() {
		t.Fatal("preview changed data")
	}
	// Refuse shared, executable and unmapped objects without attempting cleanup.
	for _, change := range []struct{ add, undo string }{
		{`CREATE TABLE public.unmapped(id int)`, `DROP TABLE public.unmapped`},
		{`GRANT SELECT ON public.calls TO adoption_reader`, `REVOKE SELECT ON public.calls FROM adoption_reader`},
		{`ALTER TABLE public.calls ALTER COLUMN conversation_id SET DEFAULT current_user`, `ALTER TABLE public.calls ALTER COLUMN conversation_id SET DEFAULT ''`},
		{`ALTER TABLE public.calls ENABLE ROW LEVEL SECURITY`, `ALTER TABLE public.calls DISABLE ROW LEVEL SECURITY`},
		{`CREATE FUNCTION public.unsafe() RETURNS int LANGUAGE sql AS 'SELECT 1'`, `DROP FUNCTION public.unsafe()`},
		{`ALTER DEFAULT PRIVILEGES FOR ROLE adoption_legacy GRANT SELECT ON TABLES TO adoption_reader`, `ALTER DEFAULT PRIVILEGES FOR ROLE adoption_legacy REVOKE SELECT ON TABLES FROM adoption_reader`},
		{`GRANT adoption_legacy TO adoption_reader`, `REVOKE adoption_legacy FROM adoption_reader`},
	} {
		exec(appAdmin, change.add)
		if preview("adopt_app", r).Executable {
			t.Fatal("unsafe catalog was accepted")
		}
		exec(appAdmin, change.undo)
	}
	// Metadata drift invalidates a previously executable preview, with no role
	// creation, ownership transfer or data changes.
	p = preview("adopt_app", r)
	assertReady(p)
	exec(legacy, `ALTER TABLE public.calls ADD COLUMN drift int`)
	values := map[string]string{"migration_password": passwords["migration_password"], "runtime_password": passwords["runtime_password"]}
	if _, e := execute("adopt_app", r, p, values); e == nil {
		t.Fatal("stale adoption plan executed")
	}
	var roleExists bool
	if admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, p.App.OwnerRole).Scan(&roleExists) != nil || roleExists {
		t.Fatal("stale plan created roles")
	}
	exec(legacy, `ALTER TABLE public.calls DROP COLUMN drift`)
	p = preview("adopt_app", r)
	assertReady(p)
	lock, e := legacy.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = lock.Exec(ctx, `LOCK TABLE public.calls IN ACCESS SHARE MODE`); e != nil {
		t.Fatal(e)
	}
	if _, e = execute("adopt_app", r, p, values); e == nil {
		t.Fatal("adoption ignored conflicting table lock")
	}
	_ = lock.Rollback(ctx)
	if admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, p.App.OwnerRole).Scan(&roleExists) != nil || roleExists {
		t.Fatal("lock refusal created roles")
	}
	r.IssuanceID = strings.Repeat("b", 32)
	p = preview("adopt_app", r)
	assertReady(p)
	result, e := execute("adopt_app", r, p, values)
	if e != nil {
		t.Fatal(e)
	}
	if result.Database != a.Database || result.Schema != a.Schema || before != fingerprint() {
		t.Fatal("adoption changed names or data")
	}
	if _, e = execute("adopt_app", r, p, values); e == nil {
		t.Fatal("adoption replay accepted")
	}
	runtime := connect(result.RuntimeLogin, passwords["runtime_password"], a.Database, "new_consumer")
	migration := connect(result.MigrationLogin, passwords["migration_password"], a.Database, "migration_consumer")
	exec(runtime, `SELECT * FROM public.history`)
	exec(runtime, `INSERT INTO public.calls VALUES('after','{}','')`)
	exec(runtime, `UPDATE public.calls SET conversation_id='changed' WHERE id='after'`)
	for _, sql := range []string{`DELETE FROM public.calls`, `TRUNCATE public.calls`, `INSERT INTO public.history(version) VALUES(4)`, `CREATE TABLE public.denied(id int)`, `CREATE TEMP TABLE denied(id int)`, "SET ROLE " + ident(result.OwnerRole), "SET ROLE " + ident(result.MigrationLogin)} {
		deny(runtime, sql)
	}
	exec(legacy, `UPDATE public.calls SET conversation_id='overlap' WHERE id='after'`)
	deny(legacy, `CREATE TABLE public.denied(id int)`)
	deny(migration, `CREATE TABLE public.before_set_role(id int)`)
	exec(migration, "SET ROLE "+ident(result.OwnerRole))
	exec(migration, `CREATE TABLE public.future(id int)`)
	exec(migration, `CREATE FUNCTION public.private_function() RETURNS int LANGUAGE sql AS 'SELECT 1'`)
	exec(runtime, `INSERT INTO public.future VALUES(1)`)
	deny(runtime, `DELETE FROM public.future`)
	deny(runtime, `SELECT public.private_function()`)
	for _, db := range []string{"postgres", "legacy_test"} {
		for _, user := range []struct{ name, password string }{{result.RuntimeLogin, passwords["runtime_password"]}, {result.MigrationLogin, passwords["migration_password"]}} {
			cc := cfg
			cc.User = user.name
			cc.Password = user.password
			cc.Database = db
			pc, _ := connectionConfig(cc)
			if c, e := pgx.ConnectConfig(ctx, pc); e == nil {
				closeConn(c)
				t.Fatal("adopted identity connected to another database")
			}
		}
	}
	// Existing lifecycle operations discover the mapping; generation names stay
	// in the managed app namespace while connections use the original database.
	rot := LifecycleRequest{AppRequest: r.AppRequest, Kind: "runtime", IssuanceID: strings.Repeat("c", 32), DryRun: true}
	rp := preview("rotate_login", rot)
	assertReady(rp)
	if rp.App.Database != a.Database || rp.App.Schema != "public" {
		t.Fatal("rotation lost mapping")
	}
	// ACL drift after preview must not create the next role.
	exec(appAdmin, "GRANT DELETE ON public.calls TO "+ident(p.App.RuntimeRole))
	if _, e := execute("rotate_login", rot, rp, map[string]string{"password": passwords["password"]}); e == nil {
		t.Fatal("rotation accepted stale ACLs")
	}
	exec(appAdmin, "REVOKE DELETE ON public.calls FROM "+ident(p.App.RuntimeRole))
	rot.IssuanceID = strings.Repeat("d", 32)
	rp = preview("rotate_login", rot)
	assertReady(rp)
	rr, e := execute("rotate_login", rot, rp, map[string]string{"password": passwords["password"]})
	if e != nil {
		t.Fatal(e)
	}
	next := connect(rr.Login, passwords["password"], a.Database, "new_generation")
	exec(next, `SELECT * FROM public.calls`)
	ret := LifecycleRequest{AppRequest: r.AppRequest, Kind: "runtime", Generation: 1, Consumer: "new_generation", DryRun: true}
	retp := preview("retire_login", ret)
	assertReady(retp)
	if _, e = execute("retire_login", ret, retp, nil); e != nil {
		t.Fatal(e)
	}
	deny(runtime, `SELECT 1`)
	mrot := LifecycleRequest{AppRequest: r.AppRequest, Kind: "migration", IssuanceID: strings.Repeat("e", 32), DryRun: true}
	mp := preview("rotate_login", mrot)
	assertReady(mp)
	mr, e := execute("rotate_login", mrot, mp, map[string]string{"password": passwords["password"]})
	if e != nil {
		t.Fatal(e)
	}
	mnext := connect(mr.Login, passwords["password"], a.Database, "next_migration")
	deny(mnext, `CREATE TABLE public.before_set_role(id int)`)
	exec(mnext, "SET ROLE "+ident(result.OwnerRole))
	exec(mnext, `BEGIN; CREATE TABLE public.migration_two(id int); ROLLBACK;`)
	mret := LifecycleRequest{AppRequest: r.AppRequest, Kind: "migration", Generation: 1, Consumer: "next_migration", DryRun: true}
	mp = preview("retire_login", mret)
	assertReady(mp)
	if _, e = execute("retire_login", mret, mp, nil); e != nil {
		t.Fatal(e)
	}
	deny(migration, `SELECT 1`)
	// A legacy cutoff without a named replacement refuses. A valid cutoff only
	// affects this DB: test DB's existing session and fresh authentication survive.
	cutoff := LifecycleRequest{AppRequest: r.AppRequest, Consumer: "missing_consumer", DryRun: true}
	if preview("retire_legacy", cutoff).Executable {
		t.Fatal("legacy retirement without proof accepted")
	}
	cutoff.Consumer = "new_generation"
	cp := preview("retire_legacy", cutoff)
	assertReady(cp)
	cr, e := execute("retire_legacy", cutoff, cp, nil)
	if e != nil || cr.RemainingSessions != 0 {
		t.Fatalf("legacy retirement failed: %v", e)
	}
	deny(legacy, `SELECT 1`)
	exec(legacyTest, `INSERT INTO public.preserved VALUES(1)`)
	freshTest := connect("adoption_legacy", passwords["legacy"], "legacy_test", "fresh_test_consumer")
	exec(freshTest, `SELECT * FROM public.preserved`)
	cc := cfg
	cc.User = a.LegacyOwner
	cc.Password = passwords["legacy"]
	cc.Database = a.Database
	pc, _ := connectionConfig(cc)
	if c, e := pgx.ConnectConfig(ctx, pc); e == nil {
		closeConn(c)
		t.Fatal("legacy reconnected to adopted DB")
	}
	var afterVerifier string
	var enabled bool
	if admin.QueryRow(ctx, `SELECT rolpassword,rolcanlogin FROM pg_authid WHERE rolname='adoption_legacy'`).Scan(&afterVerifier, &enabled) != nil || afterVerifier != legacyVerifier || !enabled {
		t.Fatal("legacy global login/password changed")
	}
	rev := LifecycleRequest{AppRequest: r.AppRequest, Kind: "runtime", Generation: 2, DryRun: true}
	vp := preview("revoke_login", rev)
	assertReady(vp)
	if _, e = execute("revoke_login", rev, vp, nil); e != nil {
		t.Fatal(e)
	}
	deny(next, `SELECT 1`)
	// Stored marker corruption cannot redirect a lifecycle operation.
	var saved string
	if admin.QueryRow(ctx, `SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=$1`, a.Database).Scan(&saved) != nil {
		t.Fatal("marker unavailable")
	}
	var marker ownership
	_ = json.Unmarshal([]byte(saved), &marker)
	marker.Adoption.Database = "legacy_test"
	exec(admin, commentSQL("DATABASE", a.Database, marker))
	if _, e = b.Preview(ctx, "rotate_login", rot); e == nil {
		t.Fatal("mismatched database marker accepted")
	}
	exec(admin, "COMMENT ON DATABASE "+ident(a.Database)+" IS "+literal(saved))
	for _, name := range []string{result.RuntimeLogin, result.MigrationLogin, rr.Login, mr.Login} {
		var v string
		if admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, name).Scan(&v) != nil {
			t.Fatal("verifier unavailable")
		}
		canaries = append(canaries, v)
	}
	for _, v := range passwords {
		canaries = append(canaries, v)
	}
	time.Sleep(150 * time.Millisecond)
	log, e := os.ReadFile(filepath.Join(socket, "server.log"))
	if e != nil {
		t.Fatal(e)
	}
	for i, v := range canaries {
		if strings.Contains(string(log), v) {
			t.Fatalf("adoption credential canary %d escaped to server logs", i)
		}
	}
	t.Log("verified metadata refusals, stale-plan safety, preserved data, scoped grants, mapped rotation/retirement/revocation and legacy test DB continuity")
}
