package digitaloceanplugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	droplets []DropletStatus
	err      error
	// getErr fails only GetDroplet, to model a create whose read-back fails.
	getErr    error
	createdID int
	calls     []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) ListDroplets(context.Context) (DropletList, error) {
	f.record("ListDroplets")
	return DropletList{Droplets: f.droplets}, f.err
}

func (f *fakeBackend) GetDroplet(_ context.Context, id int) (*DropletStatus, error) {
	f.record("GetDroplet", id)
	if f.err != nil {
		return nil, f.err
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, d := range f.droplets {
		if d.ID == id {
			return &d, nil
		}
	}
	return &DropletStatus{ID: id, Name: "web", Status: "active"}, nil
}

func (f *fakeBackend) CreateDroplet(_ context.Context, req CreateRequest) (int, error) {
	f.record("CreateDroplet", req)
	if f.err != nil {
		return 0, f.err
	}
	if f.createdID == 0 {
		f.createdID = 17
	}
	return f.createdID, nil
}

func (f *fakeBackend) PowerOn(_ context.Context, id int) error {
	f.record("PowerOn", id)
	return f.err
}

func (f *fakeBackend) PowerOff(_ context.Context, id int) error {
	f.record("PowerOff", id)
	return f.err
}

func (f *fakeBackend) Delete(_ context.Context, id int) error {
	f.record("Delete", id)
	return f.err
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

func decode[T any](t *testing.T, result subprocess.MCPCallResult) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(result.Content, &out); err != nil {
		t.Fatalf("decode %s: %v", result.Content, err)
	}
	return out
}

// validArgs is a complete, acknowledged argument set for every operation, so a
// test can iterate the manifest without knowing each schema.
func validArgs(operation string) map[string]any {
	switch operation {
	case "list_droplets":
		return map[string]any{}
	case "create_droplet":
		return map[string]any{"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64", argAcknowledged: true}
	case "start", "stop", "destroy":
		return map[string]any{"droplet_id": float64(42), argAcknowledged: true}
	default:
		return map[string]any{"droplet_id": float64(42)}
	}
}

func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for _, op := range Definition().Operations {
		if _, err := callTool(p, op.Name, validArgs(op.Name)); err != nil {
			t.Fatalf("operation %q is declared but not served: %v", op.Name, err)
		}
	}
}

// The operation set must match the compiled-in connector's, so a caller sees
// the same contract either side of the move.
func TestOperationSetMatchesTheBuiltIn(t *testing.T) {
	var got []string
	for _, op := range Definition().Operations {
		got = append(got, op.Name)
	}
	want := []string{"list_droplets", "get_droplet", "create_droplet", "start", "stop", "destroy", "status"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

// The contract is the source of truth: the plugin gates on acknowledgment
// exactly the operations the contract says need it, and previews exactly the
// ones it says preview. start is acknowledgment-gated (lifecycle) with no
// preview, which is the one case where the two sets differ.
func TestGatesMatchTheContract(t *testing.T) {
	wantEffect := map[string]string{
		"list_droplets": "read", "get_droplet": "read", "status": "read",
		"create_droplet": "write", "start": "lifecycle", "stop": "lifecycle", "destroy": "destructive",
	}
	for _, op := range Definition().Operations {
		if string(op.Effect) != wantEffect[op.Name] {
			t.Errorf("%s: effect = %s, want %s (the built-in's)", op.Name, op.Effect, wantEffect[op.Name])
		}
		if op.RequiresAck != ackOperations[op.Name] || op.Effect.ReadOnly() == ackOperations[op.Name] {
			t.Errorf("%s: effect=%s requires_ack=%v, but ackOperations says %v", op.Name, op.Effect, op.RequiresAck, ackOperations[op.Name])
		}
		if op.SupportsDry != previewOperations[op.Name] {
			t.Errorf("%s: supports_dry=%v, but previewOperations says %v", op.Name, op.SupportsDry, previewOperations[op.Name])
		}
		if op.Destructive != (op.Name == "destroy") {
			t.Errorf("%s: Destructive=%v; only destroy is destructive", op.Name, op.Destructive)
		}
	}
	for op := range previewOperations {
		if !ackOperations[op] {
			t.Errorf("%s previews but is not acknowledgment-gated", op)
		}
	}
	create := Definition().Operations[2]
	if create.Name != "create_droplet" || create.Cost != "billable" {
		t.Errorf("create_droplet cost = %s, want billable", create.Cost)
	}
}

func TestListDropletsReturnsDTOs(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	backend := &fakeBackend{droplets: []DropletStatus{{ID: 1, Name: "a", Status: "active", CreatedAt: created}}}
	got := decode[DropletList](t, mustCall(t, loadedPlugin(t, backend), "list_droplets", nil))
	if len(got.Droplets) != 1 || got.Droplets[0].ID != 1 || !got.Droplets[0].CreatedAt.Equal(created) || got.Truncated {
		t.Fatalf("list = %+v", got)
	}
}

// Over MCP and the API a droplet id arrives as a JSON number (float64); older
// CLI paths send a string. Both reach the backend as the same int.
func TestDropletIDAcceptsEveryShape(t *testing.T) {
	for _, raw := range []any{float64(42), 42, int64(42), "42"} {
		backend := &fakeBackend{}
		mustCall(t, loadedPlugin(t, backend), "get_droplet", map[string]any{"droplet_id": raw})
		if len(backend.calls) != 1 || backend.calls[0].args[0] != 42 {
			t.Fatalf("droplet_id %#v reached the backend as %+v", raw, backend.calls)
		}
	}
}

func TestDropletIDIsValidated(t *testing.T) {
	for _, raw := range []any{nil, "", "abc", float64(4.5), float64(-1), float64(0)} {
		backend := &fakeBackend{}
		args := map[string]any{}
		if raw != nil {
			args["droplet_id"] = raw
		}
		result, err := callTool(loadedPlugin(t, backend), "get_droplet", args)
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "droplet_id") {
			t.Errorf("droplet_id %#v: %s %q, want invalid_args naming droplet_id", raw, code, message)
		}
		if len(backend.calls) != 0 {
			t.Errorf("droplet_id %#v reached the backend", raw)
		}
	}
}

func TestCreateDropletSendsTheRequestAndReadsItBack(t *testing.T) {
	backend := &fakeBackend{createdID: 99}
	args := validArgs("create_droplet")
	args["ssh_keys"] = []any{"aa:bb", " cc:dd "}
	args["user_data"] = "#cloud-config\n"
	got := decode[DropletStatus](t, mustCall(t, loadedPlugin(t, backend), "create_droplet", args))
	if got.ID != 99 {
		t.Fatalf("result = %+v, want the read-back droplet", got)
	}
	want := CreateRequest{Name: "web-1", Region: "nyc3", Size: "s-1vcpu-1gb", Image: "ubuntu-24-04-x64", SSHKeys: []string{"aa:bb", "cc:dd"}, UserData: "#cloud-config\n"}
	if backend.calls[0].method != "CreateDroplet" || !reflect.DeepEqual(backend.calls[0].args[0], want) {
		t.Fatalf("create request = %+v", backend.calls[0])
	}
}

// A created droplet is billable. If reading it back fails, the create is still
// reported as a success carrying the id, never as an error inviting a retry
// and a second droplet. The compiled-in connector did the same.
func TestCreateDropletReportsTheIDWhenTheReadBackFails(t *testing.T) {
	backend := &fakeBackend{createdID: 77, getErr: errors.New("read failed")}
	result := mustCall(t, loadedPlugin(t, backend), "create_droplet", validArgs("create_droplet"))
	// The host strips telemetry before the result reaches a caller.
	content, _ := cerbplugin.SplitTelemetry(result.Content)
	if string(content) != `{"droplet_id":77}` {
		t.Fatalf("result = %s, want only the droplet id", result.Content)
	}
}

func TestCreateDropletRequiresEveryField(t *testing.T) {
	backend := &fakeBackend{}
	result, err := callTool(loadedPlugin(t, backend), "create_droplet", map[string]any{argAcknowledged: true})
	code, message := failure(t, result, err)
	if code != cerbplugin.ErrorInvalidArgs {
		t.Fatalf("code = %q, want invalid_args", code)
	}
	for _, field := range []string{"name", "region", "size", "image"} {
		if !strings.Contains(message, field+" is required") {
			t.Fatalf("message = %q, want %s reported", message, field)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend called: %+v", backend.calls)
	}
}

func TestLifecycleOperationsReportWhatTheyDid(t *testing.T) {
	cases := map[string]string{"start": "PowerOn", "stop": "PowerOff", "destroy": "Delete"}
	actions := map[string]string{"start": "power_on", "stop": "power_off", "destroy": "destroy"}
	for op, method := range cases {
		backend := &fakeBackend{}
		got := decode[DropletAction](t, mustCall(t, loadedPlugin(t, backend), op, validArgs(op)))
		if got != (DropletAction{DropletID: 42, Action: actions[op]}) {
			t.Errorf("%s: result = %+v", op, got)
		}
		if len(backend.calls) != 1 || backend.calls[0].method != method || backend.calls[0].args[0] != 42 {
			t.Errorf("%s: backend calls = %+v", op, backend.calls)
		}
	}
}

// status returns the normalized resource state as a bare string, as the
// compiled-in connector did.
func TestStatusNormalizesTheDropletState(t *testing.T) {
	for status, want := range map[string]string{"active": "running", "off": "stopped", "new": "starting", "archive": "destroyed", "weird": "unknown"} {
		backend := &fakeBackend{droplets: []DropletStatus{{ID: 42, Status: status}}}
		if got := decode[string](t, mustCall(t, loadedPlugin(t, backend), "status", validArgs("status"))); got != want {
			t.Errorf("status %q = %q, want %q", status, got, want)
		}
	}
}

func TestUnacknowledgedWriteIsRefused(t *testing.T) {
	for op := range ackOperations {
		t.Run(op, func(t *testing.T) {
			backend := &fakeBackend{}
			args := validArgs(op)
			delete(args, argAcknowledged)
			_, err := callTool(loadedPlugin(t, backend), op, args)
			if err == nil || !strings.Contains(err.Error(), "requires acknowledgment") {
				t.Fatalf("err = %v, want an acknowledgment refusal", err)
			}
			if len(backend.calls) != 0 {
				t.Fatalf("backend called without acknowledgment: %+v", backend.calls)
			}
		})
	}
}

// A dry run never reaches DigitalOcean, needs no acknowledgment and no
// credential, and returns the host's preview shape with the built-in's
// summaries, targets and inputs.
func TestDryRunPreviewsWithoutCallingDigitalOcean(t *testing.T) {
	const userData = "#cloud-config\npackages: [nginx]\n"
	cases := []struct {
		op   string
		args map[string]any
		want DryRunPreview
	}{
		{
			op: "create_droplet",
			args: map[string]any{"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64",
				"ssh_keys": []any{"aa:bb"}, "user_data": userData},
			want: DryRunPreview{DryRun: true, Connector: "digitalocean", Operation: "create_droplet", Summary: "Would create a DigitalOcean droplet.",
				Target: map[string]any{"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64"},
				Input: map[string]any{"ssh_keys": []any{"aa:bb"}, "user_data": map[string]any{
					"bytes":  float64(len(userData)),
					"sha256": userDataDigest(userData)["sha256"],
				}}},
		},
		{
			op:   "create_droplet",
			args: map[string]any{"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64"},
			want: DryRunPreview{DryRun: true, Connector: "digitalocean", Operation: "create_droplet", Summary: "Would create a DigitalOcean droplet.",
				Target: map[string]any{"name": "web-1", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64"},
				Input:  map[string]any{"ssh_keys": nil, "user_data": map[string]any{"bytes": float64(0)}}},
		},
		{
			op:   "stop",
			args: map[string]any{"droplet_id": "42"},
			want: DryRunPreview{DryRun: true, Connector: "digitalocean", Operation: "stop", Summary: "Would power off a DigitalOcean droplet.",
				Target: map[string]any{"droplet_id": float64(42)}},
		},
		{
			op:   "destroy",
			args: map[string]any{"droplet_id": float64(42)},
			want: DryRunPreview{DryRun: true, Connector: "digitalocean", Operation: "destroy", Summary: "Would destroy a DigitalOcean droplet.",
				Target: map[string]any{"droplet_id": float64(42)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			p := &Plugin{newBackend: func(string) Backend { t.Fatal("backend built for a dry run"); return nil }}
			if _, err := p.Load(context.Background()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.args[argDryRun] = true
			got := decode[DryRunPreview](t, mustCall(t, p, tc.op, tc.args))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("preview =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

// Cloud-init user_data routinely carries credentials. A preview lands in agent
// context and logs, so it describes the script and never carries it, and no
// error path echoes it either.
func TestUserDataNeverLeavesThePlugin(t *testing.T) {
	const sentinel = "SENTINEL-USER-DATA-6c2e"
	userData := "#cloud-config\nwrite_files:\n  - content: " + sentinel + "\n"

	args := validArgs("create_droplet")
	args["user_data"] = userData
	args[argDryRun] = true
	preview := mustCall(t, loadedPlugin(t, &fakeBackend{}), "create_droplet", args)
	if strings.Contains(string(preview.Content), sentinel) || strings.Contains(string(preview.Content), "cloud-config") {
		t.Fatalf("preview carries the user_data content:\n%s", preview.Content)
	}

	// An invalid call and a failing backend: neither error may carry it.
	bad := map[string]any{"user_data": userData, argAcknowledged: true}
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "create_droplet", bad)
	if _, message := failure(t, result, err); strings.Contains(message, sentinel) || strings.Contains(string(result.Content), sentinel) {
		t.Fatalf("invalid create carried the user_data: %s", message)
	}
	args = validArgs("create_droplet")
	args["user_data"] = userData
	if _, err := callTool(loadedPlugin(t, &fakeBackend{err: errors.New("422 unprocessable")}), "create_droplet", args); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatalf("failed create: err = %v", err)
	}
}

func TestUserDataDigestMatchesTheHost(t *testing.T) {
	// The host's rule, for a known input: sha256("hello\n").
	got := userDataDigest("hello\n")
	if got["bytes"] != 6 || got["sha256"] != "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03" {
		t.Fatalf("digest = %+v", got)
	}
	if !reflect.DeepEqual(userDataDigest(""), map[string]any{"bytes": 0}) {
		t.Fatalf("empty digest = %+v", userDataDigest(""))
	}
}

// An operation with no preview refuses --dry-run rather than running. start
// is the case that matters: it is acknowledgment-gated, and a dry run of it
// must not power the droplet on.
func TestDryRunWithoutAPreviewIsRefused(t *testing.T) {
	for _, op := range []string{"list_droplets", "start", "status"} {
		backend := &fakeBackend{}
		args := validArgs(op)
		args[argDryRun] = true
		result, err := callTool(loadedPlugin(t, backend), op, args)
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "no dry-run preview") {
			t.Errorf("%s: %s %q", op, code, message)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s: backend called: %+v", op, backend.calls)
		}
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	_, err := loadedPlugin(t, &fakeBackend{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_digitalocean_reboot"})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("err = %v", err)
	}
}

func TestBackendErrorsPropagate(t *testing.T) {
	_, err := callTool(loadedPlugin(t, &fakeBackend{err: errors.New("droplet not found")}), "get_droplet", validArgs("get_droplet"))
	if err == nil || !strings.Contains(err.Error(), "droplet not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitAdvertisesTheManifestIdentity(t *testing.T) {
	result, err := New().Init(context.Background(), subprocess.InitParams{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.ID != ConnectorID || result.Version != Version || result.Protocol != subprocess.ProtocolVersion {
		t.Fatalf("Init = %+v", result)
	}
}

func TestGeneratedPluginYAMLIsValid(t *testing.T) {
	spec := PluginYAML()
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatalf("generated plugin.yaml invalid: %v", err)
	}
	if spec.ID != ConnectorID {
		t.Fatalf("plugin id = %q, want %q", spec.ID, ConnectorID)
	}
	if !strings.HasSuffix(spec.Entrypoint.Command, BinaryName) {
		t.Fatalf("entrypoint = %q, want it to end in %q", spec.Entrypoint.Command, BinaryName)
	}
	if len(spec.Cerberus.Connector.Operations) != len(Definition().Operations) {
		t.Fatal("manifest operations drifted from the connector definition")
	}
}

// The id and secret name are what existing credential references were written
// against. Changing either would silently orphan them.
func TestIdentityMatchesTheBuiltInSoCredentialsCarryOver(t *testing.T) {
	if ConnectorID != "digitalocean" {
		t.Fatalf("ConnectorID = %q; the host resolves secrets as <id>/<name>, so this must stay digitalocean", ConnectorID)
	}
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != "api_token" || secrets[0].Env != "CERBERUS_DIGITALOCEAN_API_TOKEN" || !secrets[0].Required {
		t.Fatalf("secrets = %+v, want exactly a required api_token read as CERBERUS_DIGITALOCEAN_API_TOKEN", secrets)
	}
	if secrets[0].Name != SecretAPIToken {
		t.Fatalf("declared secret %q does not match the key the plugin reads, %q", secrets[0].Name, SecretAPIToken)
	}
}

func TestTokenComesFromHostInitConfig(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	var got string
	p := &Plugin{newBackend: func(token string) Backend { got = token; return &fakeBackend{} }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretAPIToken: "host-resolved-token"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "host-resolved-token" {
		t.Fatalf("token = %q, want the value the host resolved", got)
	}
}

func TestDirectRunFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv(TokenEnvVar, "direct-run-token")
	var got string
	p := &Plugin{newBackend: func(token string) Backend { got = token; return &fakeBackend{} }}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "direct-run-token" {
		t.Fatalf("token = %q, want the direct-run fallback", got)
	}
}

// A missing credential must not fail the load. Every DigitalOcean call then
// fails with the guidance, Health reports it, and dry runs keep working.
func TestMissingCredentialLoadsAndExplains(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	p := &Plugin{newBackend: func(string) Backend { t.Fatal("backend built without a token"); return nil }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load without a token: %v", err)
	}
	status, err := p.Health(context.Background())
	if err != nil || status.OK || !strings.Contains(status.Message, TokenEnvVar) {
		t.Fatalf("Health = %+v, %v; want not OK and naming %s", status, err, TokenEnvVar)
	}
	// Each call fails coded credential_missing, so the host reports that code
	// whatever else it knows.
	for _, op := range Definition().Operations {
		result, err := callTool(p, op.Name, validArgs(op.Name))
		if err != nil {
			t.Errorf("%s: uncoded error %v, want a coded credential_missing result", op.Name, err)
			continue
		}
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorCredentialMissing || message != errMissingCredential.Error() {
			t.Errorf("%s: %s %q, want credential_missing", op.Name, code, message)
		}
	}
	if _, err := callTool(p, "destroy", map[string]any{"droplet_id": float64(1), argDryRun: true}); err != nil {
		t.Fatalf("dry run without a credential: %v", err)
	}
}

func TestHealthReportsAConfiguredToken(t *testing.T) {
	status, err := loadedPlugin(t, &fakeBackend{}).Health(context.Background())
	if err != nil || !status.OK {
		t.Fatalf("Health = %+v, %v", status, err)
	}
}
