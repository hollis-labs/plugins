package dataplugin

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	cerb "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	"github.com/jackc/pgx/v5"
)

func TestScratchDataIsolation(t *testing.T) {
	socket := os.Getenv("CERBERUS_PG_SCRATCH_SOCKET")
	if socket == "" {
		t.Skip("disposable socket fixture not supplied")
	}
	if !strings.HasPrefix(socket, "/tmp/cerberus-pg-plugin-test.") {
		t.Fatal("refusing non-fixture endpoint")
	}
	ctx := context.Background()
	adminCfg, err := connectionConfig(Config{Cluster: "fixture", Host: socket, Port: "5432", User: "postgres", Database: "postgres", Password: "fixture-trust"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, sql := range []string{`CREATE ROLE data_fixture_reader LOGIN PASSWORD 'fixture-only-generated-secret'`, `CREATE DATABASE data_fixture`, `REVOKE ALL ON DATABASE data_fixture FROM PUBLIC`, `GRANT CONNECT ON DATABASE data_fixture TO data_fixture_reader`} {
		if _, err = admin.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	adminCfg.Database = "data_fixture"
	db, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	setup := `REVOKE CREATE ON SCHEMA public FROM PUBLIC; CREATE SCHEMA published; CREATE SCHEMA extensions; CREATE EXTENSION vector SCHEMA extensions; CREATE TABLE published.documents(id int PRIMARY KEY,body text,tenant text,embedding extensions.vector(3)); INSERT INTO published.documents VALUES(1,'alpha','a','[1,0,0]'),(2,'beta','a','[0,1,0]'),(3,'other tenant','b','[1,0,0]'); GRANT USAGE ON SCHEMA published,extensions TO data_fixture_reader; GRANT SELECT ON published.documents TO data_fixture_reader;`
	if _, err = db.Exec(ctx, setup); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Cluster: "fixture", Host: socket, Port: "5432", User: "data_fixture_reader", Database: "data_fixture", Password: "fixture-only-generated-secret"}
	call := func(p Projection, params string, c Config, limit int) (Result, error) {
		q := cerb.DataQuery{Target: "fixture", Projection: p.Name, Version: p.Version(), Parameters: json.RawMessage(params)}
		return execute(ctx, c, p, q, cerb.DataPermit{Digest: cerb.DataQueryDigest(q), ExpiresAt: time.Now().Add(2 * time.Second), MaxRows: limit, MaxBytes: 1 << 20})
	}
	p := projection()
	out, err := call(p, `{"tenant":"a"}`, cfg, 100)
	if err != nil || len(out.Rows) != 2 {
		t.Fatalf("read projection: rows=%d err=%v", len(out.Rows), err)
	}
	p.Vector = &Vector{Column: "embedding", Dimensions: 3}
	out, err = call(p, `{"tenant":"a","vector":[1,0,0],"top_k":1}`, cfg, 100)
	if err != nil || len(out.Rows) != 1 || string(out.Rows[0]) != `[1,"alpha"]` {
		t.Fatalf("vector projection: %v %s", err, out.Rows)
	}
	p.Vector = nil
	// A real lock wait must time out, and the next fresh connection must work.
	locked, lockErr := db.Begin(ctx)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	if _, lockErr = locked.Exec(ctx, `LOCK TABLE published.documents IN ACCESS EXCLUSIVE MODE`); lockErr != nil {
		t.Fatal(lockErr)
	}
	started := time.Now()
	_, lockErr = call(p, `{"tenant":"a"}`, cfg, 100)
	_ = locked.Rollback(ctx)
	if lockErr == nil || time.Since(started) > 2500*time.Millisecond {
		t.Fatal("lock deadline failed")
	}
	if out, err = call(p, `{"tenant":"a"}`, cfg, 100); err != nil || len(out.Rows) != 2 {
		t.Fatal("canceled connection contaminated next query")
	}
	if _, err = db.Exec(ctx, `UPDATE published.documents SET body=repeat('x',1048576) WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = call(p, `{"tenant":"a"}`, cfg, 100); err == nil {
		t.Fatal("oversized cell transferred as a valid result")
	}
	if _, err = db.Exec(ctx, `UPDATE published.documents SET body='alpha' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = call(p, `{"tenant":"a"}`, cfg, 1); err == nil {
		t.Fatal("row limit not enforced")
	}
	elevated := cfg
	elevated.User = "postgres"
	if _, err = call(p, `{"tenant":"a"}`, elevated, 100); err == nil {
		t.Fatal("admin identity accepted")
	}
	if _, err = db.Exec(ctx, `GRANT INSERT ON published.documents TO data_fixture_reader`); err != nil {
		t.Fatal(err)
	}
	if _, err = call(p, `{"tenant":"a"}`, cfg, 100); err == nil {
		t.Fatal("write-capable identity accepted")
	}
	_, _ = db.Exec(ctx, `REVOKE INSERT ON published.documents FROM data_fixture_reader`)
	if _, err = db.Exec(ctx, `ALTER TABLE published.documents ENABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if _, err = call(p, `{"tenant":"a"}`, cfg, 100); err == nil {
		t.Fatal("RLS function execution path accepted")
	}
	// A caller-supplied permit, SQL, or endpoint never substitutes for private Init.
	plugin := New()
	definitions, _ := json.Marshal([]Projection{projection()})
	_, err = plugin.Init(ctx, subprocess.InitParams{Config: map[string]string{"cluster": "fixture", "host": socket, "port": "5432", "user": cfg.User, "database": cfg.Database, "password": cfg.Password, "projections": string(definitions)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = plugin.query(ctx, subprocess.MCPCallRequest{ToolName: "cerberus_postgres-data_query", Arguments: map[string]any{"target": "fixture", "projection": "documents", "version": projection().Version(), "parameters": map[string]any{"tenant": "a"}}})
	if err == nil {
		t.Fatal("direct call without host permit accepted")
	}
}
