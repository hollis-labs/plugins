package tailscaleplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// mintedValue is a made-up key value, shaped like Tailscale's.
const mintedValue = "tskey-auth-kExampleCNTRL-abcdefghijklmnopqrstuvwxyz012345"

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	keys    []AuthKey
	devices []Device
	err     error
	// mintErr fails only CreateAuthKey.
	calls []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) wrote() bool {
	for _, c := range f.calls {
		switch c.method {
		case "CreateAuthKey", "RevokeAuthKey", "DeleteDevice", "SetDeviceTags", "AuthorizeDevice":
			return true
		}
	}
	return false
}

func (f *fakeBackend) ListAuthKeys(_ context.Context, tailnet string) (AuthKeyList, error) {
	f.record("ListAuthKeys", tailnet)
	return AuthKeyList{Keys: f.keys}, f.err
}

func (f *fakeBackend) GetAuthKey(_ context.Context, tailnet, id string) (*AuthKey, error) {
	f.record("GetAuthKey", tailnet, id)
	if f.err != nil {
		return nil, f.err
	}
	for _, k := range f.keys {
		if k.ID == id {
			return &k, nil
		}
	}
	return &AuthKey{ID: id, KeyType: "auth"}, nil
}

func (f *fakeBackend) CreateAuthKey(_ context.Context, tailnet string, req MintRequest) (*MintedKey, error) {
	f.record("CreateAuthKey", tailnet, req)
	if f.err != nil {
		return nil, f.err
	}
	return &MintedKey{
		AuthKey: AuthKey{ID: "kNew1", KeyType: "auth", Reusable: req.Reusable, Ephemeral: req.Ephemeral, Preauthorized: req.Preauthorized, Tags: req.Tags},
		Key:     mintedValue,
		Note:    mintNote,
	}, nil
}

func (f *fakeBackend) RevokeAuthKey(_ context.Context, tailnet, id string) error {
	f.record("RevokeAuthKey", tailnet, id)
	return f.err
}

func (f *fakeBackend) ListDevices(_ context.Context, tailnet string) (DeviceList, error) {
	f.record("ListDevices", tailnet)
	return DeviceList{Devices: f.devices}, f.err
}

func (f *fakeBackend) GetDevice(_ context.Context, id string) (*Device, error) {
	f.record("GetDevice", id)
	if f.err != nil {
		return nil, f.err
	}
	for _, d := range f.devices {
		if d.ID == id {
			return &d, nil
		}
	}
	return &Device{ID: id, Hostname: "host-1", Tags: []string{"tag:old"}}, nil
}

func (f *fakeBackend) DeleteDevice(_ context.Context, id string) error {
	f.record("DeleteDevice", id)
	return f.err
}

func (f *fakeBackend) SetDeviceTags(_ context.Context, id string, tags []string) error {
	f.record("SetDeviceTags", id, tags)
	return f.err
}

func (f *fakeBackend) AuthorizeDevice(_ context.Context, id string) error {
	f.record("AuthorizeDevice", id)
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

// unconfiguredPlugin is a plugin that loaded with no credential at all.
func unconfiguredPlugin(t *testing.T) *Plugin {
	t.Helper()
	t.Setenv(APIKeyEnvVar, "")
	t.Setenv(OAuthClientIDEnvVar, "")
	t.Setenv(OAuthClientSecretEnvVar, "")
	p := New()
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
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
		t.Fatalf("%s: error result %s", operation, result.Content)
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
	content, _ := cerbplugin.SplitTelemetry(result.Content)
	var out T
	if err := json.Unmarshal(content, &out); err != nil {
		t.Fatalf("decode %s: %v", content, err)
	}
	return out
}

// validArgs is a complete, acknowledged call to each gated operation.
func validArgs(op string) map[string]any {
	args := map[string]any{argAcknowledged: true}
	switch op {
	case "mint_auth_key":
		args["tags"] = []any{"tag:ci"}
	case "revoke_key":
		args["key_id"] = "kOld1"
	default:
		args["device_id"] = "n123"
		if op == "set_tags" {
			args["tags"] = []any{"tag:server"}
		}
	}
	return args
}

func TestMintDefaultsAreEphemeralSingleUsePreauthorizedTaggedShortLived(t *testing.T) {
	fb := &fakeBackend{}
	p := loadedPlugin(t, fb)
	result := mustCall(t, p, "mint_auth_key", map[string]any{argAcknowledged: true, "tags": []any{"tag:ci"}, "description": "runner"})

	minted := decode[MintedKey](t, result)
	if minted.Key != mintedValue || minted.Note == "" {
		t.Errorf("minted = %+v", minted)
	}
	req := fb.calls[0].args[1].(MintRequest)
	if req.Reusable || !req.Ephemeral || !req.Preauthorized || req.TTL != time.Hour || len(req.Tags) != 1 || req.Tags[0] != "tag:ci" {
		t.Errorf("request defaults = %+v", req)
	}
	if fb.calls[0].args[0] != DefaultTailnet {
		t.Errorf("tailnet = %v, want the default", fb.calls[0].args[0])
	}
}

func TestMintHonoursRequestedOptions(t *testing.T) {
	fb := &fakeBackend{}
	p := loadedPlugin(t, fb)
	mustCall(t, p, "mint_auth_key", map[string]any{
		argAcknowledged: true, "tags": "tag:a, tag:b", "reusable": true, "ephemeral": "false",
		"preauthorized": false, "ttl_seconds": "7200", "tailnet": "example.com",
	})
	req := fb.calls[0].args[1].(MintRequest)
	if !req.Reusable || req.Ephemeral || req.Preauthorized || req.TTL != 2*time.Hour || len(req.Tags) != 2 {
		t.Errorf("request = %+v", req)
	}
	if fb.calls[0].args[0] != "example.com" {
		t.Errorf("tailnet = %v", fb.calls[0].args[0])
	}
}

func TestMintRefusesBadArguments(t *testing.T) {
	cases := map[string]map[string]any{
		"no tags":        {},
		"untagged":       {"tags": []any{"ci"}},
		"ttl too short":  {"tags": []any{"tag:ci"}, "ttl_seconds": 5},
		"ttl too long":   {"tags": []any{"tag:ci"}, "ttl_seconds": 99999999},
		"long desc":      {"tags": []any{"tag:ci"}, "description": strings.Repeat("x", 51)},
		"bad tailnet":    {"tags": []any{"tag:ci"}, "tailnet": "../evil"},
		"nonsense bool":  {"tags": []any{"tag:ci"}, "reusable": "maybe"},
		"negative ttl":   {"tags": []any{"tag:ci"}, "ttl_seconds": -1},
		"fractional ttl": {"tags": []any{"tag:ci"}, "ttl_seconds": 1.5},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			fb := &fakeBackend{}
			args[argAcknowledged] = true
			result, err := callTool(loadedPlugin(t, fb), "mint_auth_key", args)
			code, _ := failure(t, result, err)
			if code != cerbplugin.ErrorInvalidArgs {
				t.Errorf("code = %q, want invalid_args", code)
			}
			if len(fb.calls) != 0 {
				t.Errorf("backend was called: %v", fb.calls)
			}
		})
	}
}

func TestEveryWriteRequiresAcknowledgment(t *testing.T) {
	for op := range ackOperations {
		fb := &fakeBackend{}
		args := validArgs(op)
		delete(args, argAcknowledged)
		_, err := callTool(loadedPlugin(t, fb), op, args)
		if err == nil || !strings.Contains(err.Error(), "--ack") {
			t.Errorf("%s: err = %v, want an acknowledgment refusal", op, err)
		}
		if len(fb.calls) != 0 {
			t.Errorf("%s: backend called without --ack: %v", op, fb.calls)
		}
	}
}

func TestDryRunNeverWrites(t *testing.T) {
	for op := range ackOperations {
		fb := &fakeBackend{}
		args := validArgs(op)
		delete(args, argAcknowledged)
		args[argDryRun] = true
		result := mustCall(t, loadedPlugin(t, fb), op, args)
		preview := decode[DryRunPreview](t, result)
		if !preview.DryRun || preview.Operation != op || preview.Summary == "" {
			t.Errorf("%s preview = %+v", op, preview)
		}
		if fb.wrote() {
			t.Errorf("%s: dry run wrote: %v", op, fb.calls)
		}
		if strings.Contains(string(result.Content), mintedValue) {
			t.Errorf("%s preview carries a key", op)
		}
	}
}

// A mint preview is computed from the arguments alone, so it works with no
// credential and shows exactly what would be sent.
func TestMintPreviewNeedsNoCredentialAndShowsTheRequest(t *testing.T) {
	p := unconfiguredPlugin(t)
	result := mustCall(t, p, "mint_auth_key", map[string]any{argDryRun: true, "tags": []any{"tag:ci"}, "reusable": true, "ttl_seconds": 172800})
	preview := decode[DryRunPreview](t, result)
	if preview.Input["reusable"] != true || preview.Input["ttl_seconds"] != float64(172800) || len(preview.Warnings) < 3 {
		t.Errorf("preview = %+v", preview)
	}
}

func TestDevicePreviewsReadTheCurrentState(t *testing.T) {
	fb := &fakeBackend{devices: []Device{{ID: "n123", Hostname: "host-1", Tags: []string{"tag:old", "tag:keep"}}}}
	p := loadedPlugin(t, fb)
	result := mustCall(t, p, "set_tags", map[string]any{argDryRun: true, "device_id": "n123", "tags": []any{"tag:keep", "tag:new"}})
	preview := decode[DryRunPreview](t, result)
	added, _ := json.Marshal(preview.Input["added"])
	removed, _ := json.Marshal(preview.Input["removed"])
	if string(added) != `["tag:new"]` || string(removed) != `["tag:old"]` {
		t.Errorf("added %s removed %s", added, removed)
	}
	// Clearing tags is called out.
	cleared := decode[DryRunPreview](t, mustCall(t, p, "set_tags", map[string]any{argDryRun: true, "device_id": "n123"}))
	if len(cleared.Warnings) == 0 {
		t.Error("clearing every tag carries no warning")
	}
	// A device the tailnet does not have fails the preview.
	fb.err = &apiError{Status: 404, Message: "device not found"}
	r, err := callTool(p, "delete_device", map[string]any{argDryRun: true, "device_id": "nMissing"})
	if code, _ := failure(t, r, err); code != cerbplugin.ErrorInvalidArgs {
		t.Errorf("code = %q", code)
	}
}

func TestWritesReachTheBackend(t *testing.T) {
	fb := &fakeBackend{}
	p := loadedPlugin(t, fb)

	if got := decode[KeyAction](t, mustCall(t, p, "revoke_key", validArgs("revoke_key"))); got.KeyID != "kOld1" || got.Action != "revoke" {
		t.Errorf("revoke = %+v", got)
	}
	if got := decode[DeviceAction](t, mustCall(t, p, "delete_device", validArgs("delete_device"))); got.Action != "delete" || got.DeviceID != "n123" {
		t.Errorf("delete = %+v", got)
	}
	if got := decode[DeviceAction](t, mustCall(t, p, "authorize_device", validArgs("authorize_device"))); got.Action != "authorize" {
		t.Errorf("authorize = %+v", got)
	}
	if got := decode[DeviceAction](t, mustCall(t, p, "set_tags", validArgs("set_tags"))); got.Action != "set_tags" || len(got.Tags) != 1 {
		t.Errorf("set_tags = %+v", got)
	}
	var methods []string
	for _, c := range fb.calls {
		methods = append(methods, c.method)
	}
	if strings.Join(methods, ",") != "RevokeAuthKey,DeleteDevice,AuthorizeDevice,SetDeviceTags" {
		t.Errorf("calls = %v", methods)
	}
}

func TestIDsCannotEscapeTheirPathSegment(t *testing.T) {
	for _, bad := range []string{"../x", "a/b", "a b", "n1?x=1", ""} {
		fb := &fakeBackend{}
		result, err := callTool(loadedPlugin(t, fb), "get_device", map[string]any{"device_id": bad})
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%q: code = %q", bad, code)
		}
		if len(fb.calls) != 0 {
			t.Errorf("%q reached the backend", bad)
		}
	}
}

func TestReadsReturnDTOs(t *testing.T) {
	fb := &fakeBackend{
		keys:    []AuthKey{{ID: "k1", KeyType: "auth", Tags: []string{"tag:ci"}}},
		devices: []Device{{ID: "n1", Hostname: "h"}},
	}
	p := loadedPlugin(t, fb)
	if got := decode[AuthKeyList](t, mustCall(t, p, "list_auth_keys", nil)); len(got.Keys) != 1 {
		t.Errorf("keys = %+v", got)
	}
	if got := decode[DeviceList](t, mustCall(t, p, "list_devices", map[string]any{"tailnet": "example.com"})); len(got.Devices) != 1 {
		t.Errorf("devices = %+v", got)
	}
	if got := decode[Device](t, mustCall(t, p, "get_device", map[string]any{"device_id": "n1"})); got.ID != "n1" {
		t.Errorf("device = %+v", got)
	}
}

func TestReadsRefuseDryRun(t *testing.T) {
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "list_devices", map[string]any{argDryRun: true})
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
		t.Errorf("code = %q", code)
	}
}

func TestConfiguredTailnetIsUsed(t *testing.T) {
	fb := &fakeBackend{}
	p := loadedPlugin(t, fb)
	p.config = subprocess.NewConfigReader(map[string]string{ConfigTailnet: "corp.example"})
	mustCall(t, p, "list_devices", nil)
	if fb.calls[0].args[0] != "corp.example" {
		t.Errorf("tailnet = %v", fb.calls[0].args[0])
	}
	// A per-call argument wins.
	mustCall(t, p, "list_devices", map[string]any{"tailnet": "other.example"})
	if fb.calls[1].args[0] != "other.example" {
		t.Errorf("tailnet = %v", fb.calls[1].args[0])
	}
}

// With no credential, every API operation fails per call as credential_missing,
// while the plugin loads, Health answers, and the local operations are not
// refused for it.
func TestMissingCredentialFailsPerCallAndLocalOpsKeepWorking(t *testing.T) {
	p := unconfiguredPlugin(t)
	for _, op := range []string{"list_auth_keys", "list_devices", "get_device", "revoke_key", "delete_device", "set_tags", "authorize_device", "mint_auth_key"} {
		args := validArgs(op)
		if op == "list_auth_keys" || op == "list_devices" {
			args = nil
		}
		if op == "get_device" {
			args = map[string]any{"device_id": "n1"}
		}
		result, err := callTool(p, op, args)
		code, _ := failure(t, result, err)
		if code != cerbplugin.ErrorCredentialMissing {
			t.Errorf("%s: code = %q, want credential_missing", op, code)
		}
	}

	status, err := p.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, cliErr := resolveTailscale(); cliErr == nil && !status.OK {
		t.Errorf("health failed although the CLI is present: %+v", status)
	}
	if !strings.Contains(status.Message, "api_key") {
		t.Errorf("health does not name how to supply the credential: %q", status.Message)
	}

	// The local operations never go near the credential.
	t.Setenv("PATH", t.TempDir())
	result, err := callTool(p, "status", nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if code, _, coded := cerbplugin.ParseErrorResult(result.Content); coded && code == cerbplugin.ErrorCredentialMissing {
		t.Error("a local operation was refused for a missing API credential")
	}
}

func TestUpstreamFailuresAreCoded(t *testing.T) {
	cases := []struct {
		err  error
		want cerbplugin.ErrorCode
	}{
		{&apiError{Status: 401, Message: "invalid token"}, cerbplugin.ErrorCredentialMissing},
		{&apiError{Status: 404}, cerbplugin.ErrorInvalidArgs},
		{&apiError{Status: 403, Message: "forbidden"}, ""},
		{&apiError{Status: 500}, ""},
		{errors.New("boom"), ""},
	}
	for _, tc := range cases {
		result, err := callTool(loadedPlugin(t, &fakeBackend{err: tc.err}), "list_devices", nil)
		if code, _ := failure(t, result, err); code != tc.want {
			t.Errorf("%v: code = %q, want %q", tc.err, code, tc.want)
		}
	}
}

// The minted key is returned once, in the result, and registered so that text
// composed by a later failure cannot carry it out.
func TestMintedKeyIsRegisteredWithTheScrubber(t *testing.T) {
	fb := &fakeBackend{}
	p := loadedPlugin(t, fb)
	if strings.Contains(p.scrubber().text(mintedValue), "[REDACTED]") {
		t.Fatal("the key is scrubbed before it is minted")
	}
	mustCall(t, p, "mint_auth_key", validArgs("mint_auth_key"))

	fb.err = errors.New("upstream echoed " + mintedValue + " back")
	result, err := callTool(p, "list_devices", nil)
	_, message := failure(t, result, err)
	if strings.Contains(message, mintedValue) {
		t.Errorf("a later error carries the minted key: %s", message)
	}
}

// The key appears in exactly the mint result: not in a preview, not in a
// listing, and not in the telemetry the host records.
func TestKeyValueReachesOnlyTheMintResult(t *testing.T) {
	fb := &fakeBackend{keys: []AuthKey{{ID: "kNew1", KeyType: "auth"}}}
	p := loadedPlugin(t, fb)
	minted := mustCall(t, p, "mint_auth_key", validArgs("mint_auth_key"))
	_, events := cerbplugin.SplitTelemetry(minted.Content)
	eventJSON, _ := json.Marshal(events)
	if strings.Contains(string(eventJSON), mintedValue) || len(events) == 0 {
		t.Errorf("telemetry = %s", eventJSON)
	}
	for _, r := range []subprocess.MCPCallResult{
		mustCall(t, p, "list_auth_keys", nil),
		mustCall(t, p, "revoke_key", map[string]any{argDryRun: true, "key_id": "kNew1"}),
	} {
		if strings.Contains(string(r.Content), mintedValue) {
			t.Errorf("key leaked into %s", r.Content)
		}
	}
	// The result field must not be a name the host's redactor rewrites.
	for _, hazard := range redactionHazards[3:] {
		if m := hazard.pattern.FindString(string(minted.Content)); m != "" {
			t.Errorf("mint result contains a %s-shaped construct %q the host redactor rewrites", hazard.name, m)
		}
	}
}
