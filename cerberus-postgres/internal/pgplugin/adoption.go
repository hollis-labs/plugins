package pgplugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/jackc/pgx/v5"
)

// Adoption is an explicit, bounded table-only contract. It is not a general
// migration engine: unsupported code, dependencies or shared grants block it.
type TableAccess struct {
	Name   string `json:"name"`
	Access string `json:"access"` // read or read_write (SELECT/INSERT/UPDATE)
}
type AdoptionMapping struct {
	Database    string        `json:"database"`
	Schema      string        `json:"schema"`
	LegacyOwner string        `json:"legacy_owner"`
	Tables      []TableAccess `json:"tables"`
}
type AdoptionRelation struct {
	Name  string `json:"name" cerb:"untrusted"`
	Kind  string `json:"kind"`
	Owner string `json:"owner" cerb:"untrusted"`
	ACL   string `json:"acl" cerb:"untrusted"`
}
type AdoptionInventory struct {
	DatabaseOwner string             `json:"database_owner" cerb:"untrusted"`
	DatabaseACL   string             `json:"database_acl" cerb:"untrusted"`
	SchemaOwner   string             `json:"schema_owner" cerb:"untrusted"`
	SchemaACL     string             `json:"schema_acl" cerb:"untrusted"`
	Relations     []AdoptionRelation `json:"relations"`
	CatalogDigest string             `json:"catalog_digest"`
	Blockers      []string           `json:"blockers" cerb:"untrusted"`
}

var identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

func validateAdoption(a *AdoptionMapping) error {
	if a == nil {
		return errors.New("explicit adoption mapping is required")
	}
	for _, name := range []string{a.Database, a.Schema, a.LegacyOwner} {
		if !identifierPattern.MatchString(name) || strings.HasPrefix(name, "pg_") || name == "information_schema" {
			return errors.New("adoption names must be bounded, non-system PostgreSQL identifiers")
		}
	}
	if a.Database == "postgres" || strings.HasPrefix(a.Database, "template") || strings.HasPrefix(a.Database, "cb_") {
		return errors.New("cannot adopt maintenance, template or generated app databases")
	}
	if len(a.Tables) == 0 || len(a.Tables) > 32 {
		return errors.New("adoption requires 1..32 explicitly mapped tables")
	}
	seen := map[string]bool{}
	for _, table := range a.Tables {
		if !identifierPattern.MatchString(table.Name) || strings.HasPrefix(table.Name, "pg_") || seen[table.Name] || (table.Access != "read" && table.Access != "read_write") {
			return errors.New("invalid or repeated adoption table; access must be read or read_write")
		}
		seen[table.Name] = true
	}
	return nil
}
func adoptionInputSchema() map[string]any {
	table := contract.ObjectSchema(map[string]any{"name": contract.StringSchema("Existing ordinary table name."), "access": map[string]any{"type": "string", "enum": []string{"read", "read_write"}}}, "name", "access")
	return contract.ObjectSchema(map[string]any{
		"database":     contract.StringSchema("Existing dedicated database; its name is preserved."),
		"schema":       contract.StringSchema("Existing application schema, including public; its name is preserved."),
		"legacy_owner": contract.StringSchema("Existing non-administrative owner; preserved for a separate consumer cutover."),
		"tables":       map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": table},
	}, "database", "schema", "legacy_owner", "tables")
}

// Discover mappings by provenance, not by a caller's database override. v2
// markers are intentionally refused by older plugins. All managed roles carry
// the same immutable mapping; lifecycle plans bind it and the cluster identity.
func resolveAdoption(ctx context.Context, c *pgx.Conn, r LifecycleRequest, s *appState, p *Plan) error {
	var found bool
	for _, db := range s.Inventory.Databases {
		var raw string
		if c.QueryRow(ctx, `SELECT coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=$1`, db.Name).Scan(&raw) != nil {
			return errors.New("database mapping inventory failed")
		}
		m, ok := owned(raw, r, s.Inventory, "database")
		if !ok || m.Adoption == nil {
			continue
		}
		if found || m.Adoption.Database != db.Name {
			return errors.New("ambiguous or mismatched adopted database provenance")
		}
		found = true
		s.Adoption = m.Adoption
		s.LegacyRetired = m.LegacyRetired
	}
	if r.Adoption != nil && !found {
		s.Adoption = r.Adoption
	}
	if s.Adoption != nil {
		p.Database, p.Schema, p.Adoption = s.Adoption.Database, s.Adoption.Schema, s.Adoption
	}
	return nil
}

func (b *postgresBackend) readAdoption(ctx context.Context, write bool, r LifecycleRequest, s *appState) error {
	if err := validateAdoption(r.Adoption); err != nil {
		return err
	}
	c, err := b.connect(ctx, write, r.Adoption.Database)
	if err != nil {
		return errors.New("adoption catalog connection failed; reader needs CONNECT on the existing database, not application table access")
	}
	defer closeConn(c)
	tx, err := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("adoption inventory transaction failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inv, err := readAdoptionInventory(ctx, tx, r.Adoption, b.config.User)
	if err != nil {
		return err
	}
	s.AdoptionInventory = &inv
	if tx.Commit(ctx) != nil {
		return errors.New("adoption inventory transaction did not complete")
	}
	return nil
}

// Queries never read application rows, credential hashes, function bodies or
// literal defaults into the response. Catalog detail hashes bind metadata that
// does not need to be displayed. Bounded results fail closed on excess objects.
func readAdoptionInventory(ctx context.Context, tx pgx.Tx, a *AdoptionMapping, reader string) (AdoptionInventory, error) {
	inv := AdoptionInventory{Relations: []AdoptionRelation{}, Blockers: []string{}}
	fail := func() (AdoptionInventory, error) {
		return inv, errors.New("adoption catalog inventory failed or exceeded bounds")
	}
	var dbOID uint32
	var dbDetail string
	if tx.QueryRow(ctx, `SELECT oid,pg_get_userbyid(datdba),coalesce(datacl::text,''),encode(sha256(convert_to(row(oid,datdba,datistemplate,datallowconn,datconnlimit,datacl)::text,'UTF8')),'hex') FROM pg_database WHERE datname=current_database()`).Scan(&dbOID, &inv.DatabaseOwner, &inv.DatabaseACL, &dbDetail) != nil {
		return fail()
	}
	if inv.DatabaseOwner != a.LegacyOwner {
		inv.Blockers = append(inv.Blockers, "database is not owned by the declared legacy owner")
	}
	if tx.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner),coalesce(nspacl::text,'') FROM pg_namespace WHERE nspname=$1`, a.Schema).Scan(&inv.SchemaOwner, &inv.SchemaACL) != nil {
		return fail()
	}
	if inv.SchemaOwner != a.LegacyOwner && inv.SchemaOwner != "pg_database_owner" {
		inv.Blockers = append(inv.Blockers, "schema is not owned by legacy owner or pg_database_owner")
	}
	rows, err := tx.Query(ctx, `SELECT c.relname,c.relkind::text,pg_get_userbyid(c.relowner),coalesce(c.relacl::text,'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 ORDER BY c.relname LIMIT 257`, a.Schema)
	if err != nil {
		return fail()
	}
	for rows.Next() {
		var rel AdoptionRelation
		if err = rows.Scan(&rel.Name, &rel.Kind, &rel.Owner, &rel.ACL); err != nil {
			break
		}
		inv.Relations = append(inv.Relations, rel)
	}
	rows.Close()
	if err != nil || rows.Err() != nil || len(inv.Relations) > 256 {
		return fail()
	}
	tables := map[string]bool{}
	for _, t := range a.Tables {
		tables[t.Name] = true
	}
	for _, rel := range inv.Relations {
		if rel.Owner != a.LegacyOwner {
			inv.Blockers = append(inv.Blockers, "foreign relation owner: "+rel.Name)
		}
		if rel.Kind == "r" {
			if !tables[rel.Name] {
				inv.Blockers = append(inv.Blockers, "unmapped table: "+rel.Name)
			}
			delete(tables, rel.Name)
		} else if rel.Kind != "i" {
			inv.Blockers = append(inv.Blockers, "unsupported relation kind: "+rel.Name)
		}
	}
	for name := range tables {
		inv.Blockers = append(inv.Blockers, "mapped table missing: "+name)
	}
	// Only ordinary, builtin-typed tables with plain builtin btree indexes are
	// accepted. Reject executable/custom objects rather than trying to sanitize
	// them while operating as a superuser.
	checks := []struct{ name, sql string }{
		{"other application schemas", `SELECT count(*) FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' ESCAPE '\' AND nspname NOT IN ('information_schema','public',$1)`},
		{"objects outside the mapped schema", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT LIKE 'pg\_%' ESCAPE '\' AND n.nspname NOT IN ('information_schema',$1)`},
		{"unreviewed extensions", `SELECT count(*) FROM pg_extension WHERE extname<>'plpgsql'`},
		{"event triggers", `SELECT count(*) FROM pg_event_trigger`},
		{"application functions", `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname NOT LIKE 'pg\_%' ESCAPE '\' AND n.nspname<>'information_schema'`},
		{"row security, partitions, foreign tables or custom storage", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND (c.relrowsecurity OR c.relforcerowsecurity OR c.relispartition OR c.relpersistence<>'p' OR c.relkind NOT IN ('r','i') OR (c.relkind='r' AND c.relam<>(SELECT oid FROM pg_am WHERE amname='heap')) OR c.reloptions IS NOT NULL)`},
		{"triggers", `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1`},
		{"rules", `SELECT count(*) FROM pg_rewrite r JOIN pg_class c ON c.oid=r.ev_class JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1`},
		{"inheritance", `SELECT count(*) FROM pg_inherits`},
		{"custom types or column privileges", `SELECT count(*) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_type t ON t.oid=a.atttypid WHERE n.nspname=$1 AND a.attnum>0 AND NOT a.attisdropped AND (t.typnamespace<>'pg_catalog'::regnamespace OR a.attacl IS NOT NULL OR a.attgenerated<>'' OR a.attidentity<>'')`},
		{"executable defaults", `SELECT count(*) FROM pg_attrdef d JOIN pg_class c ON c.oid=d.adrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND d.adbin::text NOT LIKE '{CONST %' AND NOT (pg_get_expr(d.adbin,d.adrelid)='now()' AND d.adbin::text LIKE ('{FUNCEXPR :funcid ' || 'pg_catalog.now()'::regprocedure::oid::text || ' %'))`},
		{"non-primary/unique constraints", `SELECT count(*) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=$1 AND c.contype NOT IN ('p','u')`},
		{"custom or expression indexes", `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND (i.indexprs IS NOT NULL OR i.indpred IS NOT NULL OR NOT i.indisvalid OR NOT i.indisready OR c.relam<>(SELECT oid FROM pg_am WHERE amname='btree') OR EXISTS(SELECT 1 FROM unnest(i.indclass) o JOIN pg_opclass p ON p.oid=o WHERE p.opcnamespace<>'pg_catalog'::regnamespace))`},
		{"default privileges", `SELECT count(*) FROM pg_default_acl`},
		{"legacy role memberships", `SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member OR r.oid=m.roleid WHERE r.rolname=$2`},
		{"foreign schema grants", `SELECT count(*) FROM pg_namespace n CROSS JOIN LATERAL aclexplode(coalesce(n.nspacl,acldefault('n',n.nspowner))) a WHERE n.nspname IN ($1,'public') AND a.grantee NOT IN (0,n.nspowner,(SELECT oid FROM pg_roles WHERE rolname=$2))`},
		{"foreign table grants", `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace CROSS JOIN LATERAL aclexplode(coalesce(c.relacl,acldefault('r',c.relowner))) a WHERE n.nspname=$1 AND a.grantee NOT IN (0,c.relowner)`},
		{"foreign database grants", `SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(coalesce(d.datacl,acldefault('d',d.datdba))) a WHERE d.datname=current_database() AND a.grantee NOT IN (0,d.datdba) AND NOT (a.grantee=(SELECT oid FROM pg_roles WHERE rolname=$3) AND a.privilege_type='CONNECT' AND NOT a.is_grantable)`},
		{"database/role settings", `SELECT count(*) FROM pg_db_role_setting WHERE setdatabase=(SELECT oid FROM pg_database WHERE datname=current_database()) OR setrole=(SELECT oid FROM pg_roles WHERE rolname=$2)`},
		{"custom casts", `SELECT count(*) FROM pg_cast WHERE oid>=16384`},
		{"custom collations", `SELECT count(*) FROM pg_collation c JOIN pg_namespace n ON n.oid=c.collnamespace WHERE n.nspname NOT LIKE 'pg\_%' ESCAPE '\' AND n.nspname<>'information_schema'`},
		{"custom operators", `SELECT count(*) FROM pg_operator o JOIN pg_namespace n ON n.oid=o.oprnamespace WHERE n.nspname NOT LIKE 'pg\_%' ESCAPE '\' AND n.nspname<>'information_schema'`},
		{"foreign-owned public schema", `SELECT count(*) FROM pg_namespace WHERE nspname='public' AND nspowner NOT IN ((SELECT oid FROM pg_roles WHERE rolname=$2),'pg_database_owner'::regrole)`},
		{"large objects", `SELECT count(*) FROM pg_largeobject_metadata`},
		{"foreign servers", `SELECT count(*) FROM pg_foreign_server`},
		{"publications", `SELECT count(*) FROM pg_publication`},
		{"custom standalone types", `SELECT count(*) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname NOT LIKE 'pg\_%' ESCAPE '\' AND n.nspname<>'information_schema' AND t.typrelid=0 AND t.typelem=0`},
	}
	counts := make([]int, len(checks))
	for idx, ch := range checks {
		// Bind a fixed three-parameter CTE so checks with fewer placeholders still
		// have an unambiguous prepared parameter list.
		q := `WITH inputs AS (SELECT $1::text schema_name,$2::text owner_name,$3::text reader_name) ` + ch.sql
		if tx.QueryRow(ctx, q, a.Schema, a.LegacyOwner, reader).Scan(&counts[idx]) != nil {
			return fail()
		}
		if counts[idx] != 0 {
			inv.Blockers = append(inv.Blockers, "unsupported "+ch.name)
		}
	}
	// Bind object structure and dependencies without returning literal defaults
	// or comments. No mutable statistics (row counts, relpages, xmin) enter plans.
	var detail string
	if tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(concat_ws('|',
  (SELECT string_agg(row(n.oid,n.nspname,n.nspowner,n.nspacl)::text,'|' ORDER BY n.oid) FROM pg_namespace n WHERE n.nspname IN ('public',$1)),
  (SELECT string_agg(row(c.oid,c.relname,c.relowner,c.relkind,c.relacl,c.relrowsecurity,c.relforcerowsecurity,c.relam,c.reloptions)::text,'|' ORDER BY c.oid) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
  (SELECT string_agg(row(a.attrelid,a.attnum,a.attname,a.atttypid,a.atttypmod,a.attnotnull,a.attisdropped,a.attcollation)::text,'|' ORDER BY a.attrelid,a.attnum) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
  (SELECT string_agg(row(d.adrelid,d.adnum,d.adbin)::text,'|' ORDER BY d.adrelid,d.adnum) FROM pg_attrdef d JOIN pg_class c ON c.oid=d.adrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
  (SELECT string_agg(i::text,'|' ORDER BY i.indexrelid) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1),
  (SELECT string_agg(c::text,'|' ORDER BY c.oid) FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE n.nspname=$1),
  (SELECT string_agg(d::text,'|' ORDER BY d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.refobjsubid,d.deptype) FROM pg_depend d WHERE d.objid IN (SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1) OR d.refobjid IN (SELECT c.oid FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1))
 ),'UTF8')),'hex')`, a.Schema).Scan(&detail) != nil {
		return fail()
	}
	inv.CatalogDigest = digest([]any{dbOID, dbDetail, detail, counts})
	sort.Strings(inv.Blockers)
	return inv, nil
}

func adoptionPlan(r LifecycleRequest, s appState, l LifecyclePlan) (LifecyclePlan, error) {
	if err := validateAdoption(r.Adoption); err != nil {
		return l, err
	}
	if s.AdoptionInventory == nil {
		return l, errors.New("adoption inventory missing")
	}
	a := r.Adoption
	l.Blockers = append(l.Blockers, s.AdoptionInventory.Blockers...)
	if s.Inventory.Recovery || s.Inventory.ServerVersion/10000 != 17 {
		l.Blockers = append(l.Blockers, "adoption requires a writable PostgreSQL 17 primary")
	}
	if s.DatabaseComment != "" {
		l.Blockers = append(l.Blockers, "existing database comment must be reviewed separately; never overwrite provenance")
	}
	if digest(s.Adoption) != digest(a) {
		l.Blockers = append(l.Blockers, "app already maps to a different database")
	}
	prefix := strings.TrimSuffix(l.App.OwnerRole, "_owner")
	for _, db := range s.Inventory.Databases {
		if db.PublicConnect {
			l.Blockers = append(l.Blockers, "PUBLIC CONNECT isolation must be reviewed first: "+db.Name)
		}
		if db.Name == prefix {
			l.Blockers = append(l.Blockers, "generated app database already exists")
		}
	}
	legacyFound := false
	for _, role := range s.Inventory.Roles {
		if strings.HasPrefix(role.Name, prefix+"_") {
			l.Blockers = append(l.Blockers, "planned role namespace already exists: "+role.Name)
		}
		if role.Name == a.LegacyOwner {
			legacyFound = true
			if role.Superuser || role.CreateDB || role.CreateRole || role.Replication || role.BypassRLS {
				l.Blockers = append(l.Blockers, "legacy owner has administrative privileges")
			}
		}
	}
	if !legacyFound {
		l.Blockers = append(l.Blockers, "legacy owner is missing")
	}
	l.Steps = []string{
		"Require a fresh verified backup and quiesced migrations; existing consumers must use runtime schema verification before ownership changes.",
		"Persist separate host-issued credentials before dispatch; acquire the cluster advisory lock and bounded exclusive locks on mapped tables; repeat the exact metadata preview.",
		"In one transaction create disabled managed roles, transfer database/schema/mapped-table ownership, revoke PUBLIC and legacy DDL privileges, and grant only mapped runtime table access.",
		"Retain legacy CONNECT and the same mapped table access for the consumer transition; preserve its password, LOGIN and access to every other database.",
		"Migrations SET ROLE the non-login owner; future tables default to runtime SELECT/INSERT/UPDATE, sequences USAGE/SELECT, functions no PUBLIC EXECUTE.",
		"Verify privilege isolation, then atomically activate both logins and write versioned database/role provenance. Preserve database/schema names and all rows.",
		"Switch the consumer to the host receipt and verified TLS; prove its named runtime connection before a separate retire_legacy preview revokes only this database's legacy access.",
	}
	l.App.Blockers = nil
	l.App.PlanDigest = ""
	l.App.Executable = false
	l.Executable = len(l.Blockers) == 0
	r.DryRun = false
	r.Acknowledged = false
	r.ExpectedPlan = ""
	l.PlanDigest = digest(struct {
		Plan    LifecyclePlan
		State   appState
		Request LifecycleRequest
	}{l, s, r})
	return l, nil
}

func qualified(schema, name string) string { return pgx.Identifier{schema, name}.Sanitize() }
func tablePrivileges(t TableAccess) string {
	if t.Access == "read_write" {
		return "SELECT,INSERT,UPDATE"
	}
	return "SELECT"
}

func (b *postgresBackend) adopt(ctx context.Context, control *pgx.Conn, r LifecycleRequest, s appState, values map[string]string) (LifecycleResult, error) {
	a := r.Adoption
	c, err := b.connect(ctx, true, a.Database)
	if err != nil {
		return LifecycleResult{}, err
	}
	defer closeConn(c)
	if err = secureWriter(ctx, c); err != nil {
		return LifecycleResult{}, err
	}
	if err = refuseEventTriggers(ctx, c); err != nil {
		return LifecycleResult{}, err
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("adoption transaction")
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tables := append([]TableAccess(nil), a.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	for _, t := range tables {
		if _, err = tx.Exec(ctx, "LOCK TABLE "+qualified(a.Schema, t.Name)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
			return LifecycleResult{}, errors.New("adoption table lock unavailable; no adoption changes committed; quiesce migrations and preview again")
		}
	}
	// Refresh cluster state after acquiring relation locks, then read catalog
	// metadata in this transaction. Plugin writers are serialized by control.
	s, err = snapshot(ctx, control, r)
	if err != nil {
		return LifecycleResult{}, err
	}
	inv, err := readAdoptionInventory(ctx, tx, a, b.config.User)
	if err != nil {
		return LifecycleResult{}, err
	}
	s.AdoptionInventory = &inv
	l, err := lifecyclePlan(ctx, control, "adopt_app", r, s)
	if err != nil {
		return LifecycleResult{}, err
	}
	if !l.Executable || l.PlanDigest != r.ExpectedPlan {
		return LifecycleResult{}, errors.New("adoption plan is stale or blocked; no adoption changes committed; use a fresh issuance and preview")
	}
	p := l.App
	mv, err := scramVerifier(values["migration_password"])
	if err != nil {
		return LifecycleResult{}, err
	}
	rv, err := scramVerifier(values["runtime_password"])
	if err != nil {
		return LifecycleResult{}, err
	}
	owner := marker(r, s.Inventory, "owner", 0, p)
	owner.Ready = true
	runtime := marker(r, s.Inventory, "runtime_role", 0, p)
	runtime.Ready = true
	migration := marker(r, s.Inventory, "migration", 1, p)
	login := marker(r, s.Inventory, "runtime", 1, p)
	db := marker(r, s.Inventory, "database", 0, p)
	stmts := []string{
		"CREATE ROLE " + ident(p.OwnerRole) + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		"CREATE ROLE " + ident(p.RuntimeRole) + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		commentSQL("ROLE", p.OwnerRole, owner), commentSQL("ROLE", p.RuntimeRole, runtime),
	}
	stmts = append(stmts, createLoginStatements(p, "migration", p.MigrationLogin, migration)...)
	stmts = append(stmts, createLoginStatements(p, "runtime", p.RuntimeLogin, login)...)
	stmts = append(stmts,
		"ALTER DATABASE "+ident(p.Database)+" OWNER TO "+ident(p.OwnerRole),
		"REVOKE ALL ON DATABASE "+ident(p.Database)+" FROM PUBLIC, "+ident(a.LegacyOwner),
		"GRANT CONNECT ON DATABASE "+ident(p.Database)+" TO "+ident(p.RuntimeRole)+", "+ident(p.MigrationLogin)+", "+ident(b.config.User)+", "+ident(a.LegacyOwner),
		"ALTER SCHEMA "+ident(p.Schema)+" OWNER TO "+ident(p.OwnerRole),
		"ALTER SCHEMA public OWNER TO "+ident(p.OwnerRole),
		"REVOKE ALL ON SCHEMA public FROM PUBLIC, "+ident(a.LegacyOwner),
		"REVOKE ALL ON SCHEMA "+ident(p.Schema)+" FROM PUBLIC, "+ident(a.LegacyOwner),
		"GRANT USAGE ON SCHEMA "+ident(p.Schema)+" TO "+ident(p.RuntimeRole)+", "+ident(a.LegacyOwner),
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+ident(p.OwnerRole)+" REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+ident(p.OwnerRole)+" IN SCHEMA "+ident(p.Schema)+" GRANT SELECT,INSERT,UPDATE ON TABLES TO "+ident(p.RuntimeRole),
		"ALTER DEFAULT PRIVILEGES FOR ROLE "+ident(p.OwnerRole)+" IN SCHEMA "+ident(p.Schema)+" GRANT USAGE,SELECT ON SEQUENCES TO "+ident(p.RuntimeRole),
	)
	for _, t := range tables {
		q := qualified(p.Schema, t.Name)
		stmts = append(stmts, "ALTER TABLE "+q+" OWNER TO "+ident(p.OwnerRole), "REVOKE ALL ON TABLE "+q+" FROM PUBLIC, "+ident(a.LegacyOwner), "GRANT "+tablePrivileges(t)+" ON TABLE "+q+" TO "+ident(p.RuntimeRole)+", "+ident(a.LegacyOwner))
	}
	if runStatements(ctx, tx, stmts...) != nil {
		return LifecycleResult{}, phaseError("adoption ownership/grants (transaction rolls back unless commit is uncertain)")
	}
	if err = checkAppPrivileges(ctx, tx, p, p.RuntimeLogin, p.MigrationLogin); err != nil {
		return LifecycleResult{}, err
	}
	if err = checkMappedTables(ctx, tx, p); err != nil {
		return LifecycleResult{}, err
	}
	migration.Ready = true
	login.Ready = true
	db.Ready = true
	if runStatements(ctx, tx, "ALTER ROLE "+ident(p.MigrationLogin)+" PASSWORD "+literal(mv)+" LOGIN", "ALTER ROLE "+ident(p.RuntimeLogin)+" PASSWORD "+literal(rv)+" LOGIN", commentSQL("ROLE", p.MigrationLogin, migration), commentSQL("ROLE", p.RuntimeLogin, login), commentSQL("DATABASE", p.Database, db)) != nil {
		return LifecycleResult{}, phaseError("adoption activation")
	}
	if tx.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("adoption commit (outcome uncertain; never replay issuance)")
	}
	return LifecycleResult{Operation: "adopt_app", Database: p.Database, Schema: p.Schema, OwnerRole: p.OwnerRole, MigrationLogin: p.MigrationLogin, RuntimeLogin: p.RuntimeLogin, Next: "Existing data and names preserved. Legacy login retains mapped runtime access only; do not restart it in automatic migration mode. Switch the verified-TLS consumer using host credential_issuance references, then separately preview retire_legacy."}, nil
}

func checkMappedTables(ctx context.Context, q rowQuerier, p Plan) error {
	if p.Adoption == nil {
		return nil
	}
	for _, t := range p.Adoption.Tables {
		var safe bool
		err := q.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'SELECT') AND has_table_privilege($1,$2,'INSERT')=$3 AND has_table_privilege($1,$2,'UPDATE')=$3 AND NOT has_table_privilege($1,$2,'DELETE,TRUNCATE,REFERENCES,TRIGGER')`, p.RuntimeRole, qualified(p.Schema, t.Name), t.Access == "read_write").Scan(&safe)
		if err != nil || !safe {
			return errors.New("adopted runtime table privileges are not the reviewed mapping")
		}
	}
	return nil
}

func legacyRetirementPlan(ctx context.Context, c *pgx.Conn, r LifecycleRequest, s appState, l *LifecyclePlan, generation int) error {
	a := s.Adoption
	if a == nil {
		l.Blockers = append(l.Blockers, "no active legacy adoption mapping")
		return nil
	}
	l.Login = a.LegacyOwner
	l.Replacement = loginName(l.App, "runtime", generation)
	if generation == 0 {
		l.Blockers = append(l.Blockers, "managed runtime replacement is missing")
	}
	for _, login := range l.Logins {
		if login.Name == l.Replacement && !login.Enabled {
			l.Blockers = append(l.Blockers, "runtime replacement is disabled")
		}
	}
	if c.QueryRow(ctx, `SELECT coalesce((SELECT pid::text||':'||extract(epoch FROM backend_start)::text FROM pg_stat_activity WHERE datname=$1 AND usename=$2 AND application_name=$3 AND backend_type='client backend' ORDER BY backend_start,pid LIMIT 1),'')`, a.Database, l.Replacement, r.Consumer).Scan(&l.Witness) != nil {
		return errors.New("legacy replacement witness read failed")
	}
	if l.Witness == "" {
		l.Blockers = append(l.Blockers, "no live replacement runtime connection with declared consumer application_name")
	}
	var unsafe bool
	if c.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid OR roleid=r.oid) FROM pg_roles r WHERE rolname=$1`, a.LegacyOwner).Scan(&unsafe) != nil {
		return errors.New("legacy role inventory failed")
	}
	if unsafe {
		l.Blockers = append(l.Blockers, "legacy attributes or memberships drifted")
	}
	for _, db := range s.Inventory.Databases {
		if db.Name == a.Database && db.PublicConnect {
			l.Blockers = append(l.Blockers, "PUBLIC CONNECT prevents legacy retirement")
		}
	}
	l.Steps = []string{"Prove the replacement runtime consumer, revoke the legacy role's CONNECT/schema/table grants only in this database and commit before draining its sessions here.", "Preserve the legacy role's LOGIN, password and access to other databases. A concurrently authenticating session still requires operational verification."}
	return nil
}
func (b *postgresBackend) retireLegacy(ctx context.Context, control *pgx.Conn, r LifecycleRequest, s appState, l LifecyclePlan) (LifecycleResult, error) {
	a := s.Adoption
	if a == nil {
		return LifecycleResult{}, errors.New("adoption mapping missing")
	}
	c, err := b.connect(ctx, true, a.Database)
	if err != nil {
		return LifecycleResult{}, err
	}
	defer closeConn(c)
	if err = secureWriter(ctx, c); err != nil {
		return LifecycleResult{}, err
	}
	if err = refuseEventTriggers(ctx, c); err != nil {
		return LifecycleResult{}, err
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("legacy retirement transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	stmts := []string{"REVOKE ALL ON DATABASE " + ident(a.Database) + " FROM " + ident(a.LegacyOwner), "REVOKE ALL ON SCHEMA " + ident(a.Schema) + " FROM " + ident(a.LegacyOwner)}
	for _, table := range a.Tables {
		stmts = append(stmts, "REVOKE ALL ON TABLE "+qualified(a.Schema, table.Name)+" FROM "+ident(a.LegacyOwner))
	}
	if runStatements(ctx, tx, stmts...) != nil {
		return LifecycleResult{}, phaseError("legacy grant retirement")
	}
	var canConnect bool
	if tx.QueryRow(ctx, `SELECT has_database_privilege($1,current_database(),'CONNECT')`, a.LegacyOwner).Scan(&canConnect) != nil || canConnect {
		return LifecycleResult{}, errors.New("legacy CONNECT remains through another grant; retirement rolled back")
	}
	m, ok := owned(s.DatabaseComment, r, s.Inventory, "database")
	if !ok {
		return LifecycleResult{}, errors.New("legacy provenance unavailable")
	}
	m.LegacyRetired = true
	if runStatements(ctx, tx, commentSQL("DATABASE", a.Database, m)) != nil || tx.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("legacy retirement commit (outcome may be uncertain)")
	}
	remaining := 0
	for attempt := 0; attempt < 5; attempt++ {
		if _, err = control.Exec(ctx, `SELECT pg_terminate_backend(pid,1000) FROM pg_stat_activity WHERE datname=$1 AND usename=$2 AND pid<>pg_backend_pid()`, a.Database, a.LegacyOwner); err != nil {
			return LifecycleResult{}, phaseError("legacy session drain (database grants retired)")
		}
		if control.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=$1 AND usename=$2`, a.Database, a.LegacyOwner).Scan(&remaining) != nil {
			return LifecycleResult{}, phaseError("legacy session verification")
		}
		if remaining == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return LifecycleResult{}, phaseError("legacy session drain deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
	return LifecycleResult{Operation: "retire_legacy", Database: a.Database, Schema: a.Schema, OwnerRole: l.App.OwnerRole, Login: a.LegacyOwner, RemainingSessions: remaining, Next: fmt.Sprintf("Legacy access to this database retired; %d observed sessions remain. Global LOGIN, password and other database access preserved.", remaining)}, nil
}

// DDL event triggers run with the invoking administrator's authority. App
// migration functions may be legitimate, but event hooks require separate review.
func refuseEventTriggers(ctx context.Context, c rowQuerier) error {
	var count int
	if c.QueryRow(ctx, `SELECT count(*) FROM pg_event_trigger`).Scan(&count) != nil || count != 0 {
		return errors.New("database event triggers require a separately reviewed administration path")
	}
	return nil
}

// Bind the current adopted ACLs and legacy session identities for later plans.
// Migration-created functions/tables are allowed after initial adoption; event
// triggers still require separate review before executing administrative DDL.
func (b *postgresBackend) readManagedAdoption(ctx context.Context, write bool, op string, s *appState) error {
	if s.Adoption == nil || (op != "rotate_login" && op != "retire_legacy") {
		return nil
	}
	a := s.Adoption
	c, err := b.connect(ctx, write, a.Database)
	if err != nil {
		return err
	}
	defer closeConn(c)
	tx, err := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return errors.New("adopted ACL inventory transaction failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = refuseEventTriggers(ctx, tx); err != nil {
		return err
	}
	inv := AdoptionInventory{Relations: []AdoptionRelation{}, Blockers: []string{}}
	if tx.QueryRow(ctx, `SELECT pg_get_userbyid(datdba),coalesce(datacl::text,'') FROM pg_database WHERE datname=current_database()`).Scan(&inv.DatabaseOwner, &inv.DatabaseACL) != nil {
		return errors.New("adopted database ACL read failed")
	}
	if tx.QueryRow(ctx, `SELECT pg_get_userbyid(nspowner),coalesce(nspacl::text,'') FROM pg_namespace WHERE nspname=$1`, a.Schema).Scan(&inv.SchemaOwner, &inv.SchemaACL) != nil {
		return errors.New("adopted schema ACL read failed")
	}
	rows, err := tx.Query(ctx, `SELECT c.relname,c.relkind::text,pg_get_userbyid(c.relowner),coalesce(c.relacl::text,'') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 ORDER BY c.relname LIMIT 1025`, a.Schema)
	if err != nil {
		return errors.New("adopted relation ACL read failed")
	}
	for rows.Next() {
		var rel AdoptionRelation
		if err = rows.Scan(&rel.Name, &rel.Kind, &rel.Owner, &rel.ACL); err != nil {
			break
		}
		inv.Relations = append(inv.Relations, rel)
	}
	rows.Close()
	if err != nil || rows.Err() != nil || len(inv.Relations) > 1024 {
		return errors.New("adopted relation ACL inventory incomplete")
	}
	var detail string
	if tx.QueryRow(ctx, `SELECT encode(sha256(convert_to(concat_ws('|',
  (SELECT string_agg(d::text,'|' ORDER BY d.oid) FROM pg_default_acl d),
  (SELECT string_agg(d::text,'|' ORDER BY d.setdatabase,d.setrole) FROM pg_db_role_setting d WHERE setdatabase=(SELECT oid FROM pg_database WHERE datname=current_database()))
 ),'UTF8')),'hex')`).Scan(&detail) != nil {
		return errors.New("adopted defaults/settings inventory failed")
	}
	inv.CatalogDigest = digest([]any{inv.DatabaseOwner, inv.DatabaseACL, inv.SchemaOwner, inv.SchemaACL, inv.Relations, detail})
	s.AdoptionInventory = &inv
	if op == "retire_legacy" {
		if tx.QueryRow(ctx, `SELECT coalesce(string_agg(pid::text||':'||extract(epoch FROM backend_start)::text,',' ORDER BY pid),'') FROM pg_stat_activity WHERE datname=current_database() AND usename=$1`, a.LegacyOwner).Scan(&s.LegacySessions) != nil {
			return errors.New("legacy session inventory failed")
		}
	}
	if tx.Commit(ctx) != nil {
		return errors.New("adopted ACL inventory did not complete")
	}
	return nil
}
