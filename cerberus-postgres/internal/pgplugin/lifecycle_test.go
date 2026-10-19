package pgplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	"github.com/jackc/pgx/v5"
)

func TestLifecycleRefusesBeforeConnecting(t *testing.T) {
	b := &postgresBackend{config: Config{Cluster: "scratch"}}
	p := &Plugin{config: b.config, backend: b}
	valid := map[string]any{"cluster": "scratch", "app": "app", "environment": "dev", "issuance_id": strings.Repeat("1", 32), "expected_plan": "sha256:" + strings.Repeat("a", 64), "acknowledged": true}
	for _, change := range []map[string]any{{"password": "hidden"}, {"sql": "SELECT 1"}, {"dsn": "hidden"}, {"issuance_id": "bad"}, {"expected_plan": "bad"}, {"acknowledged": false}, {"dry_run": "false"}} {
		args := map[string]any{}
		for k, v := range valid {
			args[k] = v
		}
		for k, v := range change {
			args[k] = v
		}
		_, err := p.call(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_provision_app", Arguments: args})
		if err == nil || strings.Contains(err.Error(), "connection") {
			t.Fatalf("input reached connection: %v", err)
		}
	}
	_, err := p.call(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_provision_app", Arguments: valid})
	if err == nil || !strings.Contains(err.Error(), "issuance") {
		t.Fatal("missing handoff was not refused before connecting")
	}
}

func TestScramRequiresHostCredentialFormat(t *testing.T) {
	for _, bad := range []string{"", strings.Repeat("a", 42), strings.Repeat("é", 43), strings.Repeat("'", 43)} {
		if _, err := scramVerifier(bad); err == nil {
			t.Fatal("accepted non-host credential")
		}
	}
	password := strings.Repeat("a", 43)
	a, err := scramVerifier(password)
	if err != nil {
		t.Fatal(err)
	}
	b, err := scramVerifier(password)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || strings.Contains(a, password) || !strings.HasPrefix(a, "SCRAM-SHA-256$4096:") {
		t.Fatal("invalid verifier or reused salt")
	}
}

func TestReadProcessRefusesAdministrativeCredential(t *testing.T) {
	f := &fakeBackend{inventory: fixture()}
	p := &Plugin{config: Config{Cluster: "scratch", AdminPassword: "must-be-write-only"}, backend: f}
	_, err := p.call(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_inspect", Arguments: map[string]any{"cluster": "scratch"}})
	if err == nil || f.calls != 0 {
		t.Fatal("read process accepted an administrative credential")
	}
}

// Only the explicitly named, disposable Unix fixture is accepted. The caller
// supplies stock PG17 + vector 0.8.7 with local postgres trust and all other
// local identities using SCRAM. No live endpoint or TCP override is accepted.
func TestScratchLifecycle(t *testing.T) {
	socket := os.Getenv("CERBERUS_PG_SCRATCH_SOCKET")
	if socket == "" {
		t.Skip("scratch fixture not supplied")
	}
	if !filepath.IsAbs(socket) || !strings.HasPrefix(filepath.Base(socket), "cerberus-pg-plugin-test.") {
		t.Fatal("refusing non-scratch endpoint")
	}
	ctx := context.Background()
	adminConfig := Config{Cluster: "scratch", Host: socket, Port: "5432", User: "postgres", Database: "postgres", Password: "scratch-local-admin-trust", AdminUser: "postgres", AdminPassword: "scratch-local-admin-trust"}
	connect := func(user, password, database, consumer string) (*pgx.Conn, error) {
		cfg := adminConfig
		cfg.User = user
		cfg.Password = password
		cfg.Database = database
		pc, err := connectionConfig(cfg)
		if err != nil {
			return nil, err
		}
		pc.RuntimeParams["application_name"] = consumer
		return pgx.ConnectConfig(ctx, pc)
	}
	admin, err := connect("postgres", adminConfig.Password, "postgres", "fixture_admin")
	if err != nil {
		t.Fatal(err)
	}
	defer closeConn(admin)
	if err = secureWriter(ctx, admin); err != nil {
		t.Fatal(err)
	}
	// Keep error output secret-free even if a server echoes a statement.
	mustExec := func(c *pgx.Conn, sql string) {
		t.Helper()
		if _, e := c.Exec(ctx, sql); e != nil {
			t.Fatal("fixture statement failed")
		}
	}
	readerPassword := strings.Repeat("c", 43)
	readerVerifier, err := scramVerifier(readerPassword)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(admin, `CREATE ROLE cerberus_catalog LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD `+literal(readerVerifier))
	mustExec(admin, `GRANT pg_read_all_stats TO cerberus_catalog`)
	mustExec(admin, `GRANT EXECUTE ON FUNCTION pg_control_system() TO cerberus_catalog`)
	mustExec(admin, `REVOKE CONNECT ON DATABASE postgres FROM PUBLIC`)
	mustExec(admin, `REVOKE CONNECT ON DATABASE template1 FROM PUBLIC`)
	mustExec(admin, `GRANT CONNECT ON DATABASE postgres TO cerberus_catalog`)
	cfg := adminConfig
	cfg.User = "cerberus_catalog"
	cfg.Password = readerPassword
	b := &postgresBackend{config: cfg}
	values := map[string]string{"migration_password": strings.Repeat("m", 43), "runtime_password": strings.Repeat("r", 43)}
	canaries := []string{readerPassword, readerVerifier, values["migration_password"], values["runtime_password"]}
	r := LifecycleRequest{AppRequest: AppRequest{Cluster: "scratch", App: "tenant", Environment: "test", Vector: true}, IssuanceID: strings.Repeat("1", 32), DryRun: true}
	preview, err := b.Preview(ctx, "provision_app", r)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Executable {
		t.Fatalf("blocked fixture: %v", preview.Blockers)
	}
	before, err := b.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Preview(ctx, "provision_app", r)
	if err != nil {
		t.Fatal(err)
	}
	after, err := b.Inspect(ctx)
	if err != nil || digest(before) != digest(after) {
		t.Fatal("preview mutated catalog")
	}
	issuance := func(op string, r LifecycleRequest, values map[string]string) cerbplugin.Issuance {
		refs := map[string]string{}
		for k := range values {
			refs[k] = "file:///private/test/" + r.IssuanceID + "-" + k
		}
		return cerbplugin.Issuance{ID: r.IssuanceID, Operation: op, BindingDigest: "test-binding", References: refs}
	}
	r.DryRun = false
	r.Acknowledged = true
	r.ExpectedPlan = preview.PlanDigest
	// The role/name tuple is not enough: a host handoff for another operation
	// or ID must be refused before any database write.
	wrong := issuance("rotate_login", r, values)
	if _, err = b.Execute(ctx, "provision_app", r, wrong, values); err == nil {
		t.Fatal("wrong operation issuance accepted")
	}
	// Catalog drift invalidates the original preview without creating any app.
	mustExec(admin, `CREATE ROLE drift_marker NOLOGIN`)
	if _, err = b.Execute(ctx, "provision_app", r, issuance("provision_app", r, values), values); err == nil {
		t.Fatal("stale plan executed")
	}
	preview, err = b.Preview(ctx, "provision_app", r)
	if err != nil {
		t.Fatal(err)
	}
	r.ExpectedPlan = preview.PlanDigest
	result, err := b.Execute(ctx, "provision_app", r, issuance("provision_app", r, values), values)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	for _, canary := range canaries {
		if strings.Contains(string(data), canary) {
			t.Fatal("credential escaped in result")
		}
	}
	if _, err = b.Execute(ctx, "provision_app", r, issuance("provision_app", r, values), values); err == nil {
		t.Fatal("provision replay accepted existing objects")
	}
	p := preview.App
	var verifier string
	for _, name := range []string{p.MigrationLogin, p.RuntimeLogin} {
		if admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, name).Scan(&verifier) != nil {
			t.Fatal("missing verifier")
		}
		canaries = append(canaries, verifier)
	}
	if bad, e := connect(p.RuntimeLogin, strings.Repeat("x", 43), p.Database, "bad_password"); e == nil {
		closeConn(bad)
		t.Fatal("fixture did not enforce SCRAM")
	}
	migration, err := connect(p.MigrationLogin, values["migration_password"], p.Database, "migrator")
	if err != nil {
		t.Fatal("migration authentication failed")
	}
	defer closeConn(migration)
	runtime, err := connect(p.RuntimeLogin, values["runtime_password"], p.Database, "app_consumer")
	if err != nil {
		t.Fatal("runtime authentication failed")
	}
	defer closeConn(runtime)
	deny := func(c *pgx.Conn, sql string) {
		t.Helper()
		if _, e := c.Exec(ctx, sql); e == nil {
			t.Fatal("forbidden privilege succeeded: " + sql)
		}
	}
	deny(migration, `CREATE TABLE app.before_set_role(id int)`)
	mustExec(migration, "SET ROLE "+ident(p.OwnerRole))
	mustExec(migration, `CREATE TABLE app.items(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, body text, embedding app.vector(3))`)
	mustExec(migration, `CREATE FUNCTION app.private_function() RETURNS int LANGUAGE sql AS 'SELECT 1'`)
	mustExec(runtime, `INSERT INTO app.items(body,embedding) VALUES('one','[1,2,3]')`)
	mustExec(runtime, `UPDATE app.items SET body='two' WHERE id=1`)
	var count int
	if runtime.QueryRow(ctx, `SELECT count(*) FROM app.items`).Scan(&count) != nil || count != 1 {
		t.Fatal("default runtime table grants failed")
	}
	mustExec(runtime, `DELETE FROM app.items WHERE id=1`)
	for _, sql := range []string{`CREATE TABLE app.forbidden(id int)`, `CREATE TABLE public.forbidden(id int)`, `CREATE TEMP TABLE forbidden(id int)`, `CREATE DATABASE forbidden`, `CREATE ROLE forbidden`, `SELECT app.private_function()`, "SET ROLE " + ident(p.OwnerRole), "SET ROLE " + ident(p.MigrationLogin), "SET ROLE " + ident(p.RuntimeRole)} {
		deny(runtime, sql)
	}
	for _, user := range []string{p.RuntimeLogin, p.MigrationLogin} {
		password := values["runtime_password"]
		if user == p.MigrationLogin {
			password = values["migration_password"]
		}
		for _, db := range []string{"postgres", "template1"} {
			if cross, e := connect(user, password, db, "cross_database"); e == nil {
				closeConn(cross)
				t.Fatal("cross database CONNECT succeeded")
			}
		}
	}
	// Rotation preserves both working credentials. No automatic consumer edit.
	rot := LifecycleRequest{AppRequest: r.AppRequest, IssuanceID: strings.Repeat("2", 32), Kind: "runtime", DryRun: true}
	rot.Vector = false
	rp, err := b.Preview(ctx, "rotate_login", rot)
	if err != nil || !rp.Executable {
		t.Fatalf("rotation preview: %v %v", err, rp.Blockers)
	}
	rot.DryRun = false
	rot.Acknowledged = true
	rot.ExpectedPlan = rp.PlanDigest
	rotValues := map[string]string{"password": strings.Repeat("n", 43)}
	canaries = append(canaries, rotValues["password"])
	rr, err := b.Execute(ctx, "rotate_login", rot, issuance("rotate_login", rot, rotValues), rotValues)
	if err != nil {
		t.Fatal(err)
	}
	if admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, rr.Login).Scan(&verifier) != nil {
		t.Fatal("missing rotated verifier")
	}
	canaries = append(canaries, verifier)
	old, err := connect(p.RuntimeLogin, values["runtime_password"], p.Database, "old_consumer")
	if err != nil {
		t.Fatal("overlap lost old login")
	}
	defer closeConn(old)
	ret := LifecycleRequest{AppRequest: rot.AppRequest, Kind: "runtime", Generation: 1, Consumer: "new_consumer", DryRun: true}
	retPlan, err := b.Preview(ctx, "retire_login", ret)
	if err != nil || retPlan.Executable {
		t.Fatal("retirement without consumer proof allowed")
	}
	newRuntime, err := connect(rr.Login, rotValues["password"], p.Database, "new_consumer")
	if err != nil {
		t.Fatal("rotated authentication failed")
	}
	defer closeConn(newRuntime)
	retPlan, err = b.Preview(ctx, "retire_login", ret)
	if err != nil || !retPlan.Executable {
		t.Fatalf("retirement proof: %v %v", err, retPlan.Blockers)
	}
	ret.DryRun = false
	ret.Acknowledged = true
	ret.ExpectedPlan = retPlan.PlanDigest
	closeConn(newRuntime)
	if _, err = b.Execute(ctx, "retire_login", ret, cerbplugin.Issuance{}, nil); err == nil {
		t.Fatal("lost replacement witness accepted")
	}
	newRuntime, err = connect(rr.Login, rotValues["password"], p.Database, "new_consumer")
	if err != nil {
		t.Fatal("replacement reconnect failed")
	}
	defer closeConn(newRuntime)
	retPlan, err = b.Preview(ctx, "retire_login", ret)
	if err != nil || !retPlan.Executable {
		t.Fatal("replacement re-preview failed")
	}
	ret.ExpectedPlan = retPlan.PlanDigest
	retired, err := b.Execute(ctx, "retire_login", ret, cerbplugin.Issuance{}, nil)
	if err != nil || !retired.Disabled || retired.RemainingSessions != 0 {
		t.Fatalf("retirement failed: %v", err)
	}
	if _, e := old.Exec(ctx, `SELECT 1`); e == nil {
		t.Fatal("old session survived retirement")
	}
	if denied, e := connect(p.RuntimeLogin, values["runtime_password"], p.Database, "old_reconnect"); e == nil {
		closeConn(denied)
		t.Fatal("retired credential reconnected")
	}
	mustExec(newRuntime, `SELECT 1 FROM app.items`)
	// Emergency revocation disables the newest generation with no replacement.
	// Even unexpected privilege grants must not prevent the emergency lockout.
	mustExec(admin, "ALTER ROLE "+ident(rr.Login)+" CREATEDB")
	rev := LifecycleRequest{AppRequest: rot.AppRequest, Kind: "runtime", Generation: 2, DryRun: true}
	revPlan, err := b.Preview(ctx, "revoke_login", rev)
	if err != nil || !revPlan.Executable {
		t.Fatalf("revoke preview: %v %v", err, revPlan.Blockers)
	}
	rev.DryRun = false
	rev.Acknowledged = true
	rev.ExpectedPlan = revPlan.PlanDigest
	revoked, err := b.Execute(ctx, "revoke_login", rev, cerbplugin.Issuance{}, nil)
	if err != nil || !revoked.Disabled || revoked.RemainingSessions != 0 {
		t.Fatalf("revoke failed: %v", err)
	}
	if _, e := newRuntime.Exec(ctx, `SELECT 1`); e == nil {
		t.Fatal("session survived revocation")
	}
	if denied, e := connect(rr.Login, rotValues["password"], p.Database, "revoked_reconnect"); e == nil {
		closeConn(denied)
		t.Fatal("revoked credential reconnected")
	}
	// Exercise migration rotation too; it must still require SET ROLE owner.
	mustExec(admin, "ALTER ROLE "+ident(rr.Login)+" NOCREATEDB")
	mrot := rot
	mrot.Kind = "migration"
	mrot.IssuanceID = strings.Repeat("3", 32)
	mrot.DryRun = true
	mrot.ExpectedPlan = ""
	mp, err := b.Preview(ctx, "rotate_login", mrot)
	if err != nil || !mp.Executable {
		t.Fatal("migration rotation preview failed")
	}
	mrot.DryRun = false
	mrot.ExpectedPlan = mp.PlanDigest
	mvalues := map[string]string{"password": strings.Repeat("q", 43)}
	canaries = append(canaries, mvalues["password"])
	mr, err := b.Execute(ctx, "rotate_login", mrot, issuance("rotate_login", mrot, mvalues), mvalues)
	if err != nil {
		t.Fatal(err)
	}
	newMigration, err := connect(mr.Login, mvalues["password"], p.Database, "migration_two")
	if err != nil {
		t.Fatal("rotated migration authentication failed")
	}
	defer closeConn(newMigration)
	deny(newMigration, `CREATE TABLE app.before_set_role(id int)`)
	mustExec(newMigration, "SET ROLE "+ident(p.OwnerRole))
	mustExec(newMigration, `CREATE TABLE app.migrated(id int)`)
	if admin.QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname=$1`, mr.Login).Scan(&verifier) != nil {
		t.Fatal("missing migration verifier")
	}
	canaries = append(canaries, verifier)
	// A setup failure after CREATE DATABASE must leave both logins disabled
	// and the new database closed. Never drop or silently adopt partial state.
	partial := r
	partial.App = "partial"
	partial.IssuanceID = strings.Repeat("4", 32)
	partial.DryRun = true
	pp, err := b.Preview(ctx, "provision_app", partial)
	if err != nil || !pp.Executable {
		t.Fatal("partial fixture preview failed")
	}
	broken := *b
	broken.config.User = "cerberus_intentionally_missing_reader"
	partial.DryRun = false
	partial.ExpectedPlan = pp.PlanDigest
	if _, err = broken.Execute(ctx, "provision_app", partial, issuance("provision_app", partial, values), values); err == nil {
		t.Fatal("injected schema setup failure did not occur")
	}
	var enabled bool
	var limit int
	if admin.QueryRow(ctx, `SELECT bool_or(rolcanlogin) FROM pg_roles WHERE rolname=ANY($1)`, []string{pp.App.MigrationLogin, pp.App.RuntimeLogin}).Scan(&enabled) != nil || enabled {
		t.Fatal("partial setup enabled a login")
	}
	if admin.QueryRow(ctx, `SELECT datconnlimit FROM pg_database WHERE datname=$1`, pp.App.Database).Scan(&limit) != nil || limit != 0 {
		t.Fatal("partial setup opened database")
	}
	// Force a server error containing a verifier in the input statement. The
	// session controls must survive it without logging its SQL or parameters.
	if _, err = admin.Exec(ctx, "ALTER ROLE cerberus_deliberately_missing PASSWORD "+literal(verifier)); err == nil {
		t.Fatal("fixture error did not occur")
	}
	// logging_collector is asynchronous; wait briefly before checking its file.
	time.Sleep(150 * time.Millisecond)
	log, err := os.ReadFile(filepath.Join(socket, "server.log"))
	if err != nil {
		t.Fatal("scratch server log unavailable")
	}
	if !strings.Contains(string(log), "statement:") {
		t.Fatal("verbose logging fixture was not active")
	}
	for idx, canary := range canaries {
		if strings.Contains(string(log), canary) {
			t.Fatalf("credential/verifier canary %d escaped to PostgreSQL log", idx)
		}
	}
	t.Log(fmt.Sprintf("verified SCRAM, runtime/migration isolation, vector DML, overlap, consumer-proved retirement, emergency drain, and %d log canaries", len(canaries)))
}
