package forgeplugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	servers []Server
	sites   []Site
	script  string
	err     error
	// scriptErr fails only GetDeploymentScript, to model a preview whose
	// read fails.
	scriptErr error
	calls     []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) ListServers(context.Context) ([]Server, error) {
	f.record("ListServers")
	return f.servers, f.err
}
func (f *fakeBackend) GetServer(_ context.Context, id int) (*Server, error) {
	f.record("GetServer", id)
	if f.err != nil {
		return nil, f.err
	}
	return &Server{ID: id, Name: "web", IsReady: true}, nil
}
func (f *fakeBackend) ListSites(_ context.Context, id int) ([]Site, error) {
	f.record("ListSites", id)
	return f.sites, f.err
}
func (f *fakeBackend) GetDeploymentScript(_ context.Context, server, site int) (string, error) {
	f.record("GetDeploymentScript", server, site)
	if f.err != nil {
		return "", f.err
	}
	return f.script, f.scriptErr
}
func (f *fakeBackend) UpdateDeploymentScript(_ context.Context, server, site int, content string, autoSource bool) error {
	f.record("UpdateDeploymentScript", server, site, content, autoSource)
	return f.err
}
func (f *fakeBackend) DeploySite(_ context.Context, server, site int) error {
	f.record("DeploySite", server, site)
	return f.err
}
func (f *fakeBackend) ExecuteSiteCommand(_ context.Context, server, site int, command string) (*SiteCommand, error) {
	f.record("ExecuteSiteCommand", server, site, command)
	if f.err != nil {
		return nil, f.err
	}
	return &SiteCommand{ID: 9, ServerID: server, SiteID: site, Command: command, Status: "waiting"}, nil
}

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func callTool(p *Plugin, operation string, args map[string]any) (subprocess.MCPCallResult, error) {
	return p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
}

func mustCall(t *testing.T, p *Plugin, operation string, args map[string]any) subprocess.MCPCallResult {
	t.Helper()
	result, err := callTool(p, operation, args)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if result.IsError {
		t.Fatalf("%s: %s", operation, result.Content)
	}
	return result
}

// failure reads a failed call either way it can come back: coded, as an error
// result the host maps onto its own code, or as a plain error.
func failure(t *testing.T, result subprocess.MCPCallResult, err error) (cerbplugin.ErrorCode, string) {
	t.Helper()
	if err != nil {
		return "", err.Error()
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !result.IsError || !ok {
		t.Fatalf("call succeeded (%s), want a failure", result.Content)
	}
	return code, message
}

// callFailure calls an operation that must fail and reads the failure.
func callFailure(t *testing.T, p *Plugin, operation string, args map[string]any) (cerbplugin.ErrorCode, string) {
	t.Helper()
	result, err := callTool(p, operation, args)
	return failure(t, result, err)
}

func decode[T any](t *testing.T, result subprocess.MCPCallResult) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(result.Content, &out); err != nil {
		t.Fatalf("decode %s: %v", result.Content, err)
	}
	return out
}

// validArgs is a complete, acknowledged argument set for every operation.
func validArgs(operation string) map[string]any {
	switch operation {
	case "list_servers":
		return map[string]any{}
	case "get_server", "list_sites":
		return map[string]any{"server_id": float64(12)}
	case "get_deployment_script":
		return map[string]any{"server_id": float64(12), "site_id": float64(34)}
	case "update_deployment_script":
		return map[string]any{"server_id": float64(12), "site_id": float64(34), "content": "cd /home/forge/site\ngit pull\n", argAcknowledged: true}
	case "deploy_site":
		return map[string]any{"server_id": float64(12), "site_id": float64(34), argAcknowledged: true}
	case "exec_site_command":
		return map[string]any{"server_id": float64(12), "site_id": float64(34), "command": "php artisan migrate --force", argAcknowledged: true}
	}
	return map[string]any{}
}

func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for _, op := range Definition().Operations {
		mustCall(t, p, op.Name, validArgs(op.Name))
	}
}

func TestOperationSetMatchesTheBuiltIn(t *testing.T) {
	var got []string
	for _, op := range Definition().Operations {
		got = append(got, op.Name)
	}
	want := []string{"list_servers", "get_server", "list_sites", "get_deployment_script", "update_deployment_script", "deploy_site", "exec_site_command"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

// The contract is the source of truth: the plugin gates exactly the operations
// the contract says need acknowledgment, and every one of them previews.
func TestGatesMatchTheContract(t *testing.T) {
	wantEffect := map[string]string{
		"list_servers": "read", "get_server": "read", "list_sites": "read",
		"get_deployment_script":    "read_sensitive",
		"update_deployment_script": "write", "deploy_site": "lifecycle", "exec_site_command": "exec",
	}
	for _, op := range Definition().Operations {
		if string(op.Effect) != wantEffect[op.Name] {
			t.Errorf("%s: effect %s, want %s (the built-in's)", op.Name, op.Effect, wantEffect[op.Name])
		}
		if op.RequiresAck != writeOperations[op.Name] || op.SupportsDry != writeOperations[op.Name] {
			t.Errorf("%s: requires_ack=%v supports_dry=%v, want both %v", op.Name, op.RequiresAck, op.SupportsDry, writeOperations[op.Name])
		}
	}
	if op, _ := Definition().Operation("get_deployment_script"); op.Output != "free_text" {
		t.Errorf("get_deployment_script output = %s, want free_text", op.Output)
	}
}

func TestReadsReturnDTOs(t *testing.T) {
	backend := &fakeBackend{
		servers: []Server{{ID: 12, Name: "web", IP: "192.0.2.5", IsReady: true}},
		sites:   []Site{{ID: 34, ServerID: 12, Name: "example.com", Branch: "main"}},
		script:  "cd /home/forge/site\n",
	}
	p := loadedPlugin(t, backend)
	if got := decode[[]Server](t, mustCall(t, p, "list_servers", nil)); len(got) != 1 || got[0].IP != "192.0.2.5" {
		t.Fatalf("servers = %+v", got)
	}
	if got := decode[[]Site](t, mustCall(t, p, "list_sites", validArgs("list_sites"))); len(got) != 1 || got[0].Branch != "main" {
		t.Fatalf("sites = %+v", got)
	}
	if got := decode[string](t, mustCall(t, p, "get_deployment_script", validArgs("get_deployment_script"))); got != backend.script {
		t.Fatalf("script = %q", got)
	}
}

func TestIDsAreValidated(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"missing":      {},
		"zero":         {"server_id": float64(0)},
		"negative":     {"server_id": float64(-3)},
		"fractional":   {"server_id": 1.5},
		"not a number": {"server_id": "twelve"},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &fakeBackend{}
			code, _ := callFailure(t, loadedPlugin(t, backend), "get_server", args)
			if code != cerbplugin.ErrorInvalidArgs || len(backend.calls) != 0 {
				t.Fatalf("code = %q, calls = %v", code, backend.calls)
			}
		})
	}
	// Every shape a caller sends an id in reaches the backend as the same int.
	for _, raw := range []any{float64(12), 12, int64(12), "12"} {
		backend := &fakeBackend{}
		mustCall(t, loadedPlugin(t, backend), "get_server", map[string]any{"server_id": raw})
		if backend.calls[0].args[0] != 12 {
			t.Fatalf("%T id reached the backend as %v", raw, backend.calls[0].args[0])
		}
	}
}

func TestWritesDoWhatTheySay(t *testing.T) {
	backend := &fakeBackend{}
	p := loadedPlugin(t, backend)

	args := validArgs("update_deployment_script")
	args["auto_source"] = true
	if got := decode[SiteAction](t, mustCall(t, p, "update_deployment_script", args)); got != (SiteAction{ServerID: 12, SiteID: 34, Action: "update_deployment_script"}) {
		t.Fatalf("update = %+v", got)
	}
	if c := backend.calls[len(backend.calls)-1]; c.method != "UpdateDeploymentScript" || c.args[2] != "cd /home/forge/site\ngit pull\n" || c.args[3] != true {
		t.Fatalf("update call = %+v (the script's whitespace must reach Forge unchanged)", c)
	}
	if got := decode[SiteAction](t, mustCall(t, p, "deploy_site", validArgs("deploy_site"))); got.Action != "deploy" {
		t.Fatalf("deploy = %+v", got)
	}
	if got := decode[SiteCommand](t, mustCall(t, p, "exec_site_command", validArgs("exec_site_command"))); got.Command != "php artisan migrate --force" {
		t.Fatalf("exec = %+v", got)
	}
}

func TestUpdateRequiresContent(t *testing.T) {
	args := validArgs("update_deployment_script")
	args["content"] = "   \n"
	backend := &fakeBackend{}
	code, message := callFailure(t, loadedPlugin(t, backend), "update_deployment_script", args)
	if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "content is required") || len(backend.calls) != 0 {
		t.Fatalf("code = %q, message = %q, calls = %v", code, message, backend.calls)
	}
}

// The host refuses an unacknowledged write before it gets here. The plugin
// refuses it too, so its own contract does not depend on the caller.
func TestUnacknowledgedWriteIsRefused(t *testing.T) {
	for op := range writeOperations {
		t.Run(op, func(t *testing.T) {
			backend := &fakeBackend{}
			args := validArgs(op)
			delete(args, argAcknowledged)
			_, message := callFailure(t, loadedPlugin(t, backend), op, args)
			if !strings.Contains(message, "requires acknowledgment") || len(backend.calls) != 0 {
				t.Fatalf("message = %q, calls = %v", message, backend.calls)
			}
		})
	}
}

func TestDryRunOnAReadIsRefused(t *testing.T) {
	backend := &fakeBackend{}
	code, _ := callFailure(t, loadedPlugin(t, backend), "list_servers", map[string]any{argDryRun: true})
	if code != cerbplugin.ErrorInvalidArgs || len(backend.calls) != 0 {
		t.Fatalf("code = %q, calls = %v", code, backend.calls)
	}
}

// The deploy and command previews call nothing, and match the compiled-in
// connector's summary, target and input exactly.
func TestDeployAndExecPreviewsCallNothing(t *testing.T) {
	cases := []struct {
		op   string
		want DryRunPreview
	}{
		{"deploy_site", newPreview("deploy_site", "Would trigger a Forge site deployment.", map[string]any{"server_id": float64(12), "site_id": float64(34)}, nil)},
		{"exec_site_command", newPreview("exec_site_command", "Would execute a Forge site command.", map[string]any{"server_id": float64(12), "site_id": float64(34)}, map[string]any{"command": "php artisan migrate --force"})},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			backend := &fakeBackend{}
			args := validArgs(tc.op)
			args[argDryRun] = true
			got := decode[DryRunPreview](t, mustCall(t, loadedPlugin(t, backend), tc.op, args))
			if !reflect.DeepEqual(got, tc.want) || len(backend.calls) != 0 {
				t.Fatalf("preview =\n %+v\nwant\n %+v\ncalls %v", got, tc.want, backend.calls)
			}
		})
	}
}

// update_deployment_script's preview reads the current script (one real
// read, never a write) and returns the diff against the proposed one.
func TestUpdatePreviewDiffsAgainstTheCurrentScript(t *testing.T) {
	backend := &fakeBackend{script: "cd /home/forge/site\ngit pull origin main\ncomposer install\n"}
	args := validArgs("update_deployment_script")
	args["content"] = "cd /home/forge/site\ngit pull origin main\ncomposer install --no-dev\nphp artisan migrate --force\n"
	args[argDryRun] = true
	got := decode[DryRunPreview](t, mustCall(t, loadedPlugin(t, backend), "update_deployment_script", args))
	if len(backend.calls) != 1 || backend.calls[0].method != "GetDeploymentScript" {
		t.Fatalf("calls = %v, want exactly the one read", backend.calls)
	}
	if got.Diff == nil || !got.Diff.Changed || got.Diff.Added != 2 || got.Diff.Removed != 1 {
		t.Fatalf("diff = %+v", got.Diff)
	}
	for _, want := range []string{"-composer install\n", "+composer install --no-dev\n", "+php artisan migrate --force\n", "@@ -1,3 +1,4 @@"} {
		if !strings.Contains(got.Diff.Unified, want) {
			t.Errorf("unified diff lacks %q:\n%s", want, got.Diff.Unified)
		}
	}
	digest, _ := got.Input["content"].(map[string]any)
	if digest["bytes"] != float64(len(args["content"].(string))) || len(digest["sha256"].(string)) != 64 {
		t.Fatalf("content digest = %v", got.Input["content"])
	}
}

// Feeding back the current script is a no-op, which is what the live check
// relies on.
func TestUpdatePreviewOfTheSameScriptIsEmpty(t *testing.T) {
	backend := &fakeBackend{script: "cd /home/forge/site\ngit pull\n"}
	args := validArgs("update_deployment_script")
	args["content"] = backend.script
	args[argDryRun] = true
	got := decode[DryRunPreview](t, mustCall(t, loadedPlugin(t, backend), "update_deployment_script", args))
	if got.Diff == nil || got.Diff.Changed || got.Diff.Added+got.Diff.Removed != 0 || got.Diff.Unified != "" {
		t.Fatalf("diff = %+v, want no change", got.Diff)
	}
}

// A preview whose read fails fails the dry run, coded, rather than showing a
// diff against an empty script.
func TestUpdatePreviewFailsWhenTheReadFails(t *testing.T) {
	backend := &fakeBackend{scriptErr: &apiError{StatusCode: 404, Body: `{"message":"Not Found."}`}}
	args := validArgs("update_deployment_script")
	args[argDryRun] = true
	code, _ := callFailure(t, loadedPlugin(t, backend), "update_deployment_script", args)
	if code != cerbplugin.ErrorInvalidArgs {
		t.Fatalf("code = %q, want invalid_args for an unknown site", code)
	}
	for _, c := range backend.calls {
		if c.method == "UpdateDeploymentScript" {
			t.Fatal("a failed preview wrote")
		}
	}
}

// A missing credential does not fail the load. Every Forge call then fails
// coded credential_missing, the script preview included since it reads, and
// the deploy and command previews keep working.
func TestMissingCredentialLoadsAndExplains(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	p := &Plugin{newBackend: func(string) Backend { t.Fatal("backend built without a token"); return nil }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load without a token: %v", err)
	}
	status, _ := p.Health(context.Background())
	if status.OK || !strings.Contains(status.Message, TokenEnvVar) {
		t.Fatalf("Health = %+v", status)
	}
	for _, op := range Definition().Operations {
		args := validArgs(op.Name)
		if op.Name == "update_deployment_script" {
			args[argDryRun] = true
		}
		code, message := callFailure(t, p, op.Name, args)
		if code != cerbplugin.ErrorCredentialMissing || message != errMissingCredential.Error() {
			t.Errorf("%s: code %q message %q", op.Name, code, message)
		}
	}
	for _, op := range []string{"deploy_site", "exec_site_command"} {
		args := validArgs(op)
		args[argDryRun] = true
		mustCall(t, p, op, args)
	}
}

func TestHealthReportsAConfiguredToken(t *testing.T) {
	status, err := loadedPlugin(t, &fakeBackend{}).Health(context.Background())
	if err != nil || !status.OK {
		t.Fatalf("Health = %+v, %v", status, err)
	}
}

func TestUnknownToolIsRefused(t *testing.T) {
	_, err := loadedPlugin(t, &fakeBackend{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_forge_drop_everything"})
	if err == nil || !errors.Is(err, err) || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("err = %v", err)
	}
}
