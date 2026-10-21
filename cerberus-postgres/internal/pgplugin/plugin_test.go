package pgplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

type fakeBackend struct {
	calls     int
	inventory Inventory
}

func (f *fakeBackend) Inspect(context.Context) (Inventory, error) { f.calls++; return f.inventory, nil }
func fixture() Inventory {
	return Inventory{Cluster: "scratch", SystemID: "123", ServerVersion: 170011, VectorVersion: "0.8.7", Databases: []Database{{Name: "postgres", Owner: "postgres", PublicConnect: true}}}
}
func TestPlansExposeIsolationGapsAndBindObservedState(t *testing.T) {
	i := fixture()
	r := AppRequest{Cluster: "scratch", App: "test", Environment: "dev", Vector: true}
	p, err := BuildPlan(r, i)
	if err != nil {
		t.Fatal(err)
	}
	if p.Executable || !strings.Contains(strings.Join(p.Blockers, " "), "PUBLIC CONNECT") {
		t.Fatal("preview must identify isolation gap and remain unexecutable")
	}
	same, _ := BuildPlan(r, i)
	if same.PlanDigest != p.PlanDigest {
		t.Fatal("unstable plan")
	}
	i.Recovery = true
	changed, _ := BuildPlan(r, i)
	if changed.PlanDigest == p.PlanDigest {
		t.Fatal("state not bound")
	}
	i = fixture()
	i.Roles = []Role{{Name: p.RuntimeLogin, Login: true, Superuser: true}}
	conflict, _ := BuildPlan(r, i)
	if !strings.Contains(strings.Join(conflict.Blockers, " "), "already exists") {
		t.Fatal("role collision must refuse adoption")
	}
}
func TestNamesCannotCollideAcrossAppEnvironmentOrCluster(t *testing.T) {
	pairs := []AppRequest{{"scratch", "a_b", "c", false}, {"scratch", "a", "b_c", false}, {"other", "a_b", "c", false}}
	names := map[string]bool{}
	for _, r := range pairs {
		i := fixture()
		i.Cluster = r.Cluster
		p, err := BuildPlan(r, i)
		if err != nil {
			t.Fatal(err)
		}
		if names[p.Database] {
			t.Fatal("collision")
		}
		names[p.Database] = true
		if len(p.RuntimeRole) > 63 {
			t.Fatal("Postgres identifier truncation")
		}
	}
}
func TestInvalidArgumentsNeverReachDatabase(t *testing.T) {
	f := &fakeBackend{inventory: fixture()}
	p := &Plugin{config: Config{Cluster: "scratch"}, backend: f}
	for _, args := range []map[string]any{
		{"cluster": "other"}, {"cluster": "scratch", "sql": "select 1"}, {"cluster": "scratch", "password": "secret"},
		{"cluster": "scratch", "app": "a; DROP ROLE x", "environment": "dev"}, {"cluster": "scratch", "app": "a", "environment": "dev", "vector": "false"},
	} {
		_, err := p.call(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_plan_app", Arguments: args})
		if err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid request touched database")
	}
}
func TestTransportCannotInheritPlaintextFallback(t *testing.T) {
	t.Setenv("PGSSLMODE", "prefer")
	t.Setenv("PGHOST", "attacker.invalid")
	t.Setenv("PGOPTIONS", "-c search_path=public")
	t.Setenv("PGDATABASE", "other")
	c := Config{Cluster: "scratch", Host: "postgres.example.test", Port: "5432", User: "reader", Database: "postgres", Password: "secret"}
	cfg, err := connectionConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != c.Host || cfg.Database != c.Database || cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify || cfg.TLSConfig.ServerName != c.Host || len(cfg.Fallbacks) != 0 || cfg.RuntimeParams["search_path"] != "pg_catalog" || cfg.RuntimeParams["options"] != "" {
		t.Fatal("ambient connection settings survived")
	}
	c.Host = "/tmp/scratch-socket"
	cfg, err = connectionConfig(c)
	if err != nil || cfg.TLSConfig != nil {
		t.Fatal("explicit Unix transport unavailable")
	}
}
func TestMissingCredentialsLoadWithoutLeakingConfig(t *testing.T) {
	p := New()
	_, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{"cluster": "scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, err := p.Health(context.Background())
	if err != nil || h.OK {
		t.Fatal("missing configuration must remain visible")
	}
}
func TestResultContainsOnlyPlanMetadata(t *testing.T) {
	f := &fakeBackend{inventory: fixture()}
	p := &Plugin{config: Config{Cluster: "scratch", Password: "canary-do-not-print"}, backend: f}
	r, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_postgres_plan_app", Arguments: map[string]any{"cluster": "scratch", "app": "app", "environment": "dev"}})
	if err != nil || r.IsError {
		t.Fatal(err)
	}
	if strings.Contains(string(r.Content), p.config.Password) {
		t.Fatal("credential escaped")
	}
	var plan Plan
	if json.Unmarshal(r.Content, &plan) != nil || plan.Executable {
		t.Fatal("invalid plan result")
	}
}
