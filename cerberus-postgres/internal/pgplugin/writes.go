package pgplugin

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var issuedPassword = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Accept only the host's high-entropy ASCII format, so there is no SASLprep
// ambiguity. Plaintext never appears in SQL; the verifier is secret too.
func scramVerifier(password string) (string, error) {
	if !issuedPassword.MatchString(password) {
		return "", errors.New("invalid host-issued credential format")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("credential verifier entropy unavailable")
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, 4096, 32)
	if err != nil {
		return "", errors.New("credential verifier derivation failed")
	}
	mac := hmac.New(sha256.New, salted)
	_, _ = mac.Write([]byte("Client Key"))
	stored := sha256.Sum256(mac.Sum(nil))
	mac = hmac.New(sha256.New, salted)
	_, _ = mac.Write([]byte("Server Key"))
	server := mac.Sum(nil)
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$4096:" + b64(salt) + "$" + b64(stored[:]) + ":" + b64(server), nil
}

// Password statements require a dedicated superuser connection with stock
// PostgreSQL logging controls. Refuse preload hooks instead of claiming to
// suppress an unknown audit extension. These SETs contain no credentials.
// Session settings (not SET LOCAL) survive a failed password transaction.
func secureWriter(ctx context.Context, c *pgx.Conn) error {
	var super bool
	var hooks string
	err := c.QueryRow(ctx, `SELECT rolsuper,current_setting('shared_preload_libraries') || current_setting('session_preload_libraries') || current_setting('local_preload_libraries') FROM pg_roles WHERE rolname=current_user`).Scan(&super, &hooks)
	if err != nil || !super || hooks != "" {
		return errors.New("writes require a dedicated superuser and no preload libraries; custom audit hooks need a separately reviewed credential-safe path")
	}
	var extensions int
	if c.QueryRow(ctx, `SELECT count(*) FROM pg_extension WHERE extname NOT IN ('plpgsql','vector')`).Scan(&extensions) != nil || extensions != 0 {
		return errors.New("unreviewed maintenance-database extensions prevent credential-safe writes")
	}
	for _, sql := range []string{
		`SET log_statement='none'`, `SET log_min_duration_statement=-1`, `SET log_min_duration_sample=-1`, `SET log_statement_sample_rate=0`,
		`SET log_duration=off`, `SET log_parameter_max_length=0`, `SET log_parameter_max_length_on_error=0`,
		`SET log_min_error_statement='panic'`, `SET log_error_verbosity='terse'`, `SET log_min_messages='panic'`,
		`SET standard_conforming_strings=on`,
	} {
		if _, err = c.Exec(ctx, sql); err != nil {
			return errors.New("cannot establish credential-safe PostgreSQL session logging")
		}
	}
	return nil
}

func runStatements(ctx context.Context, tx pgx.Tx, statements ...string) error {
	for _, sql := range statements {
		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}

func createLoginStatements(p Plan, kind, name string, m ownership) []string {
	parent := p.RuntimeRole
	inheritance := "INHERIT"
	options := "INHERIT TRUE, SET FALSE, ADMIN FALSE"
	if kind == "migration" {
		parent = p.OwnerRole
		inheritance = "NOINHERIT"
		options = "INHERIT FALSE, SET TRUE, ADMIN FALSE"
	}
	return []string{
		"CREATE ROLE " + ident(name) + " NOLOGIN " + inheritance + " NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		"GRANT " + ident(parent) + " TO " + ident(name) + " WITH " + options,
		"ALTER ROLE " + ident(name) + " SET search_path=pg_catalog," + ident(p.Schema),
		commentSQL("ROLE", name, m),
	}
}

func (b *postgresBackend) provision(ctx context.Context, c *pgx.Conn, r LifecycleRequest, i Inventory, p Plan, values map[string]string) (LifecycleResult, error) {
	migrationVerifier, err := scramVerifier(values["migration_password"])
	if err != nil {
		return LifecycleResult{}, err
	}
	runtimeVerifier, err := scramVerifier(values["runtime_password"])
	if err != nil {
		return LifecycleResult{}, err
	}
	owner := marker(r, i, "owner", 0)
	owner.Ready = true
	runtime := marker(r, i, "runtime_role", 0)
	runtime.Ready = true
	migrationLogin := marker(r, i, "migration", 1)
	runtimeLogin := marker(r, i, "runtime", 1)
	database := marker(r, i, "database", 0)
	tx, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("role transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := []string{
		"CREATE ROLE " + ident(p.OwnerRole) + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		"CREATE ROLE " + ident(p.RuntimeRole) + " NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS",
		commentSQL("ROLE", p.OwnerRole, owner), commentSQL("ROLE", p.RuntimeRole, runtime),
	}
	statements = append(statements, createLoginStatements(p, "migration", p.MigrationLogin, migrationLogin)...)
	statements = append(statements, createLoginStatements(p, "runtime", p.RuntimeLogin, runtimeLogin)...)
	if runStatements(ctx, tx, statements...) != nil || tx.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("role preparation")
	}
	// No non-superuser can connect during the nontransactional CREATE DATABASE
	// interval, even before PUBLIC is revoked. template0 avoids inherited DDL.
	if _, err = c.Exec(ctx, "CREATE DATABASE "+ident(p.Database)+" OWNER "+ident(p.OwnerRole)+" TEMPLATE template0 CONNECTION LIMIT 0"); err != nil {
		return LifecycleResult{}, phaseError("database creation (logins remain disabled)")
	}
	if _, err = c.Exec(ctx, commentSQL("DATABASE", p.Database, database)); err != nil {
		return LifecycleResult{}, phaseError("database provenance (logins remain disabled)")
	}
	app, err := b.connect(ctx, true, p.Database)
	if err != nil {
		return LifecycleResult{}, phaseError("new database connection (logins remain disabled)")
	}
	defer closeConn(app)
	if err = secureWriter(ctx, app); err != nil {
		return LifecycleResult{}, err
	}
	atx, err := app.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("schema transaction")
	}
	defer func() { _ = atx.Rollback(ctx) }()
	statements = []string{
		"REVOKE ALL ON DATABASE " + ident(p.Database) + " FROM PUBLIC",
		"GRANT CONNECT ON DATABASE " + ident(p.Database) + " TO " + ident(p.RuntimeRole) + ", " + ident(p.MigrationLogin) + ", " + ident(b.config.User),
		"REVOKE ALL ON SCHEMA public FROM PUBLIC",
		"CREATE SCHEMA app AUTHORIZATION " + ident(p.OwnerRole),
		"GRANT USAGE ON SCHEMA app TO " + ident(p.RuntimeRole),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ident(p.OwnerRole) + " REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ident(p.OwnerRole) + " IN SCHEMA app GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO " + ident(p.RuntimeRole),
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + ident(p.OwnerRole) + " IN SCHEMA app GRANT USAGE,SELECT ON SEQUENCES TO " + ident(p.RuntimeRole),
	}
	if p.Vector {
		statements = append(statements, "CREATE EXTENSION vector WITH SCHEMA app VERSION "+literal(i.VectorVersion), "REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC", "GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA app TO "+ident(p.RuntimeRole)+", "+ident(p.OwnerRole))
	}
	if runStatements(ctx, atx, statements...) != nil || atx.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("private schema setup (logins remain disabled)")
	}
	if err = checkAppPrivileges(ctx, app, p, p.RuntimeLogin, p.MigrationLogin); err != nil {
		return LifecycleResult{}, err
	}
	// Passwords and LOGIN are the final, atomic step. Host persistence happened
	// before dispatch; no generated value is ever returned from this plugin.
	final, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("login activation")
	}
	defer func() { _ = final.Rollback(ctx) }()
	migrationLogin.Ready = true
	runtimeLogin.Ready = true
	database.Ready = true
	if runStatements(ctx, final,
		"ALTER ROLE "+ident(p.MigrationLogin)+" PASSWORD "+literal(migrationVerifier)+" LOGIN",
		"ALTER ROLE "+ident(p.RuntimeLogin)+" PASSWORD "+literal(runtimeVerifier)+" LOGIN",
		commentSQL("ROLE", p.MigrationLogin, migrationLogin), commentSQL("ROLE", p.RuntimeLogin, runtimeLogin), commentSQL("DATABASE", p.Database, database),
		"ALTER DATABASE "+ident(p.Database)+" CONNECTION LIMIT -1",
	) != nil || final.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("login activation (outcome may be uncertain)")
	}
	return LifecycleResult{Operation: "provision_app", Database: p.Database, Schema: p.Schema, OwnerRole: p.OwnerRole, MigrationLogin: p.MigrationLogin, RuntimeLogin: p.RuntimeLogin, Next: "Use the host credential_issuance references. Migrations must SET ROLE the owner. Remote consumers still require reviewed TLS and network/HBA reach."}, nil
}

// Catalog checks are performed as admin, before enabling a new identity. This
// does not replace independent authentication/isolation integration tests.
type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func checkAppPrivileges(ctx context.Context, c rowQuerier, p Plan, runtime, migration string) error {
	var safe bool
	err := c.QueryRow(ctx, `SELECT
	 NOT has_database_privilege($1,current_database(),'CREATE') AND NOT has_database_privilege($1,current_database(),'TEMP')
	 AND has_database_privilege($1,current_database(),'CONNECT') AND has_schema_privilege($1,$4,'USAGE') AND NOT has_schema_privilege($1,$4,'CREATE')
	 AND NOT pg_has_role($1,$3,'SET') AND NOT pg_has_role($1,$3,'USAGE')
	 AND has_database_privilege($2,current_database(),'CONNECT') AND pg_has_role($2,$3,'SET') AND NOT pg_has_role($2,$3,'USAGE')
	 AND NOT EXISTS(SELECT 1 FROM pg_database d WHERE d.datallowconn AND d.datname<>current_database() AND (has_database_privilege($1,d.oid,'CONNECT') OR has_database_privilege($2,d.oid,'CONNECT')))
	 AND NOT EXISTS(SELECT 1 FROM pg_namespace n WHERE n.nspname NOT LIKE 'pg_%' AND has_schema_privilege($1,n.oid,'CREATE'))`, runtime, migration, p.OwnerRole, p.Schema).Scan(&safe)
	if err != nil || !safe {
		return phaseError("app privilege verification (new logins remain disabled)")
	}
	return nil
}

func (b *postgresBackend) rotate(ctx context.Context, c *pgx.Conn, r LifecycleRequest, i Inventory, l LifecyclePlan, password string) (LifecycleResult, error) {
	verifier, err := scramVerifier(password)
	if err != nil {
		return LifecycleResult{}, err
	}
	// Validate the private database before touching global roles.
	app, err := b.connect(ctx, true, l.App.Database)
	if err != nil {
		return LifecycleResult{}, phaseError("rotation database connection")
	}
	defer closeConn(app)
	if err = secureWriter(ctx, app); err != nil {
		return LifecycleResult{}, err
	}
	if err = refuseEventTriggers(ctx, app); err != nil {
		return LifecycleResult{}, err
	}
	if err = checkMappedTables(ctx, app, l.App); err != nil {
		return LifecycleResult{}, err
	}
	var generation int
	for _, login := range l.Logins {
		if login.Kind == r.Kind && login.Generation >= generation {
			generation = login.Generation + 1
		}
	}
	m := marker(r, i, r.Kind, generation, l.App)
	tx, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("rotation transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	statements := createLoginStatements(l.App, r.Kind, l.Login, m)
	if r.Kind == "migration" {
		statements = append(statements, "GRANT CONNECT ON DATABASE "+ident(l.App.Database)+" TO "+ident(l.Login))
	}
	if runStatements(ctx, tx, statements...) != nil || tx.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("rotation preparation")
	}
	// The new role is visible but cannot authenticate. Check it with an
	// existing opposite-kind login, including cross-database CONNECT denial.
	runtime, migration := l.Login, ""
	if r.Kind == "migration" {
		migration = l.Login
		runtime = ""
	}
	for _, login := range l.Logins {
		if login.Kind == "migration" && migration == "" {
			migration = login.Name
		}
		if login.Kind == "runtime" && runtime == "" {
			runtime = login.Name
		}
	}
	if err = checkAppPrivileges(ctx, app, l.App, runtime, migration); err != nil {
		return LifecycleResult{}, err
	}
	final, err := c.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, phaseError("rotation activation")
	}
	defer func() { _ = final.Rollback(ctx) }()
	m.Ready = true
	if runStatements(ctx, final, "ALTER ROLE "+ident(l.Login)+" PASSWORD "+literal(verifier)+" LOGIN", commentSQL("ROLE", l.Login, m)) != nil || final.Commit(ctx) != nil {
		return LifecycleResult{}, phaseError("rotation activation (outcome may be uncertain)")
	}
	return LifecycleResult{Operation: "rotate_login", Database: l.App.Database, Schema: l.App.Schema, OwnerRole: l.App.OwnerRole, Login: l.Login, Next: "Previous generations remain enabled. Switch the consumer to the new host credential reference, reconnect with a named application_name, then preview retire_login."}, nil
}

func disableLogin(ctx context.Context, c *pgx.Conn, op string, l LifecyclePlan) (LifecycleResult, error) {
	// Commit NOLOGIN before termination; cancellation cannot undo the lockout.
	if _, err := c.Exec(ctx, "ALTER ROLE "+ident(l.Login)+" NOLOGIN"); err != nil {
		return LifecycleResult{}, phaseError("login disable")
	}
	remaining := 0
	for attempt := 0; attempt < 5; attempt++ {
		_, err := c.Exec(ctx, `SELECT pg_terminate_backend(pid,1000) FROM pg_stat_activity WHERE usename=$1 AND pid<>pg_backend_pid()`, l.Login)
		if err != nil {
			return LifecycleResult{}, phaseError("session drain (login disabled; verify remaining sessions)")
		}
		if c.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE usename=$1`, l.Login).Scan(&remaining) != nil {
			return LifecycleResult{}, phaseError("session drain verification (login disabled)")
		}
		if remaining == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return LifecycleResult{}, phaseError("session drain deadline (login disabled)")
		case <-time.After(100 * time.Millisecond):
		}
	}
	next := "Login disabled and observed sessions drained. Keep the credential receipt for audit; no role or data was dropped."
	if remaining > 0 {
		next = "Login disabled, but " + strconv.Itoa(remaining) + " sessions remain; investigate and retry a fresh revoke plan."
	}
	return LifecycleResult{Operation: op, Database: l.App.Database, Schema: l.App.Schema, OwnerRole: l.App.OwnerRole, Login: l.Login, Disabled: true, RemainingSessions: remaining, Next: next}, nil
}
