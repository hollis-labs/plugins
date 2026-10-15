package caddyplugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// sentinelToken stands in for the Cloudflare token. Tests assert it never
// reaches a Caddyfile, a diff, a result or an error.
const sentinelToken = "cf-sentinel-9f3a7c1e2b4d"

type fakeCall struct {
	bin  string
	args []string
	env  []string
	// config is the content of the --config file at the moment of the call.
	config string
}

// fakeRunner records every caddy invocation and answers from a script.
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	// reply decides each call's output; nil means success with no output.
	reply func(call fakeCall) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, bin string, args []string, env []string) ([]byte, error) {
	call := fakeCall{bin: bin, args: args, env: env}
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			if data, err := os.ReadFile(args[i+1]); err == nil {
				call.config = string(data)
			}
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(call)
}

func (f *fakeRunner) subcommands() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.args[0])
	}
	return out
}

func newTestPlugin(t *testing.T, run *fakeRunner, secrets map[string]string) *Plugin {
	t.Helper()
	p := New()
	p.run = run
	p.resolve = func() (string, error) { return "/fake/bin/caddy", nil }
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: secrets}); err != nil {
		t.Fatal(err)
	}
	return p
}

func call(t *testing.T, p *Plugin, operation string, args map[string]any) (subprocess.MCPCallResult, error) {
	t.Helper()
	return p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
}

func decode[T any](t *testing.T, res subprocess.MCPCallResult) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(res.Content, &out); err != nil {
		t.Fatalf("decode %s: %v", res.Content, err)
	}
	return out
}

func baseArgs(configPath string) map[string]any {
	return map[string]any{
		"bind":        "100.64.0.10",
		"config_path": configPath,
		"routes": []any{
			map[string]any{"name": "app", "host": "hub", "domain": "example.com", "port": float64(8080)},
			map[string]any{"name": "api", "host": "hub", "domain": "example.com", "port": float64(8090)},
		},
	}
}

func tempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Caddyfile")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertNoToken(t *testing.T, label, text string) {
	t.Helper()
	if strings.Contains(text, sentinelToken) {
		t.Errorf("%s contains the Cloudflare token:\n%s", label, text)
	}
}

// strayTemps lists staging files left beside the config.
func strayTemps(t *testing.T, configPath string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".Caddyfile.cerberus-") {
			out = append(out, e.Name())
		}
	}
	return out
}
