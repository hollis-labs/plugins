package dataplugin

import (
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

type Config struct{ Cluster, Host, Port, User, Database, CAFile, Password string }

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
	cfg.RuntimeParams = map[string]string{"application_name": "cerberus-data-query", "search_path": "pg_catalog", "statement_timeout": "2000", "lock_timeout": "1000", "idle_in_transaction_session_timeout": "5000"}
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
