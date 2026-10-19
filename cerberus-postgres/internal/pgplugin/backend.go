package pgplugin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Config struct{ Cluster, Host, Port, User, Database, CAFile, Password, AdminUser, AdminPassword string }
type Backend interface {
	Inspect(context.Context) (Inventory, error)
}
type postgresBackend struct{ config Config }

// Build from explicit fields, never a caller-supplied DSN. Clearing pgx's
// fallbacks and runtime parameters removes ambient PG* and pgservice overrides.
func connectionConfig(c Config) (*pgx.ConnConfig, error) {
	port, err := strconv.ParseUint(c.Port, 10, 16)
	if err != nil || port == 0 || c.Cluster == "" || c.Host == "" || c.User == "" || c.Database == "" || c.Password == "" {
		return nil, errors.New("PostgreSQL target configuration is incomplete")
	}
	cfg, err := pgx.ParseConfig("host=localhost port=5432 user=unused dbname=postgres password=unused sslmode=disable")
	if err != nil {
		return nil, errors.New("PostgreSQL client initialization failed")
	}
	cfg.Host = c.Host
	cfg.Port = uint16(port)
	cfg.User = c.User
	cfg.Database = c.Database
	cfg.Password = c.Password
	cfg.Fallbacks = nil
	cfg.RuntimeParams = map[string]string{"application_name": "cerberus-postgres-inspect", "search_path": "pg_catalog", "statement_timeout": "2000", "lock_timeout": "1000", "idle_in_transaction_session_timeout": "5000"}
	cfg.ConnectTimeout = 3 * time.Second
	cfg.TLSConfig = nil
	cfg.Tracer = nil
	if !filepath.IsAbs(c.Host) {
		// No plaintext TCP fallback, including localhost. Unix sockets are the
		// explicit local administrative transport while TLS is not provisioned.
		if strings.ContainsAny(c.Host, " /\\\t\r\n") {
			return nil, errors.New("invalid PostgreSQL hostname")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load TLS trust roots")
		}
		if c.CAFile != "" {
			pem, e := os.ReadFile(c.CAFile)
			if e != nil || !roots.AppendCertsFromPEM(pem) {
				return nil, errors.New("cannot load PostgreSQL CA file")
			}
		}
		cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.Host, RootCAs: roots}
	}
	// pgx may install hooks based on PG environment options. Do not inherit them.
	cfg.ValidateConnect = nil
	cfg.AfterConnect = nil
	cfg.LookupFunc = net.DefaultResolver.LookupHost
	return cfg, nil
}

func (b *postgresBackend) Inspect(ctx context.Context) (Inventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, err := connectionConfig(b.config)
	if err != nil {
		return Inventory{}, err
	}
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return Inventory{}, errors.New("PostgreSQL catalog connection failed")
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.Close(closeCtx)
	}()
	return inspectConn(ctx, c, b.config.Cluster)
}

func inspectConn(ctx context.Context, c *pgx.Conn, cluster string) (Inventory, error) {
	tx, err := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Inventory{}, errors.New("PostgreSQL catalog transaction failed")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	i := Inventory{Cluster: cluster, Databases: []Database{}, Roles: []Role{}}
	err = tx.QueryRow(ctx, `SELECT system_identifier::text, current_setting('server_version_num')::int, pg_is_in_recovery(), coalesce((SELECT default_version FROM pg_available_extensions WHERE name='vector'),'') FROM pg_control_system()`).Scan(&i.SystemID, &i.ServerVersion, &i.Recovery, &i.VectorVersion)
	if err != nil {
		return Inventory{}, errors.New("PostgreSQL identity read failed; catalog reader needs EXECUTE on pg_control_system")
	}
	rows, err := tx.Query(ctx, `SELECT datname,pg_get_userbyid(datdba),datallowconn AND EXISTS(SELECT 1 FROM aclexplode(coalesce(datacl,acldefault('d',datdba))) a WHERE a.grantee=0 AND a.privilege_type='CONNECT') FROM pg_database ORDER BY datname LIMIT 257`)
	if err != nil {
		return Inventory{}, errors.New("PostgreSQL database catalog read failed")
	}
	for rows.Next() {
		var d Database
		if err = rows.Scan(&d.Name, &d.Owner, &d.PublicConnect); err != nil {
			break
		}
		i.Databases = append(i.Databases, d)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return Inventory{}, errors.New("PostgreSQL database catalog decode failed")
	}
	if len(i.Databases) > 256 {
		return Inventory{}, errors.New("database inventory exceeds limit; no truncated plan returned")
	}
	rows, err = tx.Query(ctx, `SELECT rolname,rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_roles WHERE rolname NOT LIKE 'pg\_%' ESCAPE '\' ORDER BY rolname LIMIT 1025`)
	if err != nil {
		return Inventory{}, errors.New("PostgreSQL role catalog read failed")
	}
	for rows.Next() {
		var r Role
		if err = rows.Scan(&r.Name, &r.Login, &r.Superuser, &r.CreateDB, &r.CreateRole, &r.Replication, &r.BypassRLS); err != nil {
			break
		}
		i.Roles = append(i.Roles, r)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return Inventory{}, errors.New("PostgreSQL role catalog decode failed")
	}
	if len(i.Roles) > 1024 {
		return Inventory{}, errors.New("role inventory exceeds limit; no truncated plan returned")
	}
	if tx.Commit(ctx) != nil {
		return Inventory{}, errors.New("PostgreSQL catalog transaction did not complete")
	}
	return i, nil
}
