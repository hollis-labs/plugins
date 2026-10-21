package pgplugin

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Only a Unix socket in an explicitly named scratch fixture is accepted.
// This suite never executes DDL, queries app rows or reads pg_authid.
func TestScratchCatalogAndPlan(t *testing.T) {
	socket := os.Getenv("CERBERUS_PG_SCRATCH_SOCKET")
	if socket == "" {
		t.Skip("scratch fixture not supplied")
	}
	if !strings.Contains(socket, "cerberus-pg-plugin-test.") {
		t.Fatal("refusing non-scratch endpoint")
	}
	b := &postgresBackend{config: Config{Cluster: "scratch", Host: socket, Port: "5432", User: "postgres", Database: "postgres", Password: "scratch-unused-local-trust"}}
	i, err := b.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if i.SystemID == "" || i.VectorVersion != "0.8.7" {
		t.Fatal("missing real cluster/extension metadata")
	}
	p, err := BuildPlan(AppRequest{Cluster: "scratch", App: "example", Environment: "test", Vector: true}, i)
	if err != nil {
		t.Fatal(err)
	}
	after, err := b.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if digest(i) != digest(after) || p.Executable {
		t.Fatal("preview changed catalog or claimed to apply")
	}
}
