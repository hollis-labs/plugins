package tailscaleplugin

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The install review shows no gaps for the API operations. These are the
// review's gap rules (internal/pluginhost/review.go in cerberus), applied to
// the generated plugin.yaml. The local-CLI operations answer in plain text and
// declare no telemetry, which the review reports as a gap it explains.
func TestInstallReviewShowsNoUnexpectedGaps(t *testing.T) {
	spec := PluginYAML()
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	block := spec.Cerberus
	if gaps := block.Connector.ContractGaps(); len(gaps) != 0 {
		t.Errorf("contract gaps: %v", gaps)
	}
	telemetry := map[string]bool{}
	for _, decl := range block.Telemetry {
		telemetry[decl.Operation] = true
	}
	for _, op := range block.Connector.Operations {
		if op.Output == "" {
			t.Errorf("%s declares no output kind", op.Name)
		}
		if !op.EffectiveEffect().ReadOnly() && !localOps[op.Name] && !telemetry[op.Name] {
			t.Errorf("%s changes something and declares no telemetry", op.Name)
		}
		if localOps[op.Name] && telemetry[op.Name] {
			t.Errorf("%s answers in plain text and cannot report telemetry, yet declares it", op.Name)
		}
	}
	if block.Host != (cerbplugin.HostRange{MinContract: 1, MaxContract: 1}) {
		t.Errorf("host range %+v", block.Host)
	}
	if err := block.Host.Check(cerbplugin.ContractVersion); err != nil {
		t.Errorf("the current host is outside the declared range: %v", err)
	}
	// MCP is suggested for plain reads and the long-listed local operations,
	// never for minting, revoking or deleting.
	for _, name := range block.Surfaces.MCP {
		op, _ := cerbplugin.OperationFromToolName(ConnectorID, cerbplugin.ToolNameForOperation(ConnectorID, name), block.Connector)
		if op.EffectiveEffect() != contract.EffectRead && !localOps[name] {
			t.Errorf("%s is suggested for MCP", name)
		}
	}
	rules := map[string]bool{}
	for _, rule := range block.SuggestedPolicy {
		rules[rule.Operation] = true
	}
	for name := range ackOperations {
		if !rules[name] {
			t.Errorf("%s has no suggested policy", name)
		}
	}
}

// Every API write reports an event of a kind it declares, and a read reports
// nothing.
func TestOperationsReportTheirDeclaredTelemetry(t *testing.T) {
	declared := map[string]map[string]bool{}
	for _, decl := range PluginYAML().Cerberus.Telemetry {
		declared[decl.Operation] = map[string]bool{}
		for _, e := range decl.Events {
			declared[decl.Operation][e] = true
		}
	}
	for _, op := range Manifest().Operations {
		if localOps[op.Name] {
			continue
		}
		for _, dry := range []bool{false, true} {
			req := subprocess.MCPCallRequest{ToolName: cerbplugin.ToolNameForOperation(ConnectorID, op.Name), Arguments: map[string]any{argDryRun: dry}}
			out := withOperationTelemetry(req, subprocess.MCPCallResult{Content: []byte(`{"ok":true}`)})
			_, events := cerbplugin.SplitTelemetry(out.Content)
			if op.EffectiveEffect().ReadOnly() {
				if len(events) != 0 {
					t.Errorf("read %s reported %v", op.Name, events)
				}
				continue
			}
			if len(events) == 0 {
				t.Errorf("%s reported nothing", op.Name)
			}
			for _, e := range events {
				if !declared[op.Name][e.Kind] {
					t.Errorf("%s reported %q, which it does not declare", op.Name, e.Kind)
				}
			}
		}
	}
}

func TestOperationSet(t *testing.T) {
	var got []string
	for _, op := range Definition().Operations {
		got = append(got, op.Name)
	}
	want := []string{"status", "up", "down", "serve", "list_auth_keys", "mint_auth_key", "revoke_key",
		"list_devices", "get_device", "delete_device", "set_tags", "authorize_device"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

// The contract is the source of truth: the plugin gates exactly the API writes
// the contract says need acknowledgment, and every one of them has a preview.
// Minting, revoking and deleting are destructive.
func TestGatesMatchTheContract(t *testing.T) {
	destructive := map[string]bool{"mint_auth_key": true, "revoke_key": true, "delete_device": true}
	for _, op := range Definition().Operations {
		if localOps[op.Name] {
			if op.Name != "status" && (!op.RequiresAck || op.SupportsDry) {
				t.Errorf("local %s: requires_ack=%v supports_dry=%v; want ack with no preview, as before", op.Name, op.RequiresAck, op.SupportsDry)
			}
			continue
		}
		if op.RequiresAck != ackOperations[op.Name] {
			t.Errorf("%s: requires_ack=%v, but ackOperations says %v", op.Name, op.RequiresAck, ackOperations[op.Name])
		}
		if op.SupportsDry != ackOperations[op.Name] {
			t.Errorf("%s: supports_dry=%v; every write needs a real preview and no read has one", op.Name, op.SupportsDry)
		}
		if op.Destructive != destructive[op.Name] {
			t.Errorf("%s: Destructive=%v, want %v", op.Name, op.Destructive, destructive[op.Name])
		}
	}
}

// Every declared operation is served. A call with valid arguments must not
// reach "declared but not served".
func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for _, op := range Definition().Operations {
		if localOps[op.Name] {
			continue
		}
		args := validArgs(op.Name)
		if op.Name == "get_device" {
			args = map[string]any{"device_id": "n1"}
		}
		result, err := callTool(p, op.Name, args)
		if err != nil || result.IsError {
			t.Errorf("%s: %v %s", op.Name, err, result.Content)
		}
	}
}

// The declared secrets are what the plugin reads, none is required (either
// credential kind is enough), and each has the env name the host resolves.
func TestDeclaredSecretsMatchWhatThePluginReads(t *testing.T) {
	want := map[string]string{
		SecretAPIKey:            "CERBERUS_TAILSCALE_API_KEY",
		SecretOAuthClientID:     "CERBERUS_TAILSCALE_OAUTH_CLIENT_ID",
		SecretOAuthClientSecret: "CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET",
	}
	got := map[string]string{}
	for _, s := range Definition().Config.Secrets {
		got[s.Name] = s.Env
		if s.Required {
			t.Errorf("secret %s is required, but either credential kind is enough", s.Name)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secrets = %v, want %v", got, want)
	}
}

func TestCredentialsComeFromHostInitConfig(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	t.Setenv(OAuthClientIDEnvVar, "")
	t.Setenv(OAuthClientSecretEnvVar, "")
	var got Credentials
	p := &Plugin{newBackend: func(c Credentials) Backend { got = c; return &fakeBackend{} }}
	_, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{
		SecretOAuthClientID: "client-id-from-host", SecretOAuthClientSecret: "client-secret-from-host",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.OAuthClientID != "client-id-from-host" || got.OAuthClientSecret != "client-secret-from-host" || got.APIKey != "" {
		t.Fatalf("credentials = %+v", got)
	}
	if p.backend == nil {
		t.Fatal("no backend built")
	}
}

// Half an OAuth client is no credential.
func TestHalfAnOAuthClientIsNotACredential(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "")
	t.Setenv(OAuthClientIDEnvVar, "")
	t.Setenv(OAuthClientSecretEnvVar, "")
	p := New()
	_, _ = p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretOAuthClientID: "only-the-id"}})
	_, _ = p.Load(context.Background())
	if p.backend != nil {
		t.Fatal("a backend was built from half a credential")
	}
}

func TestDirectRunFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv(APIKeyEnvVar, "direct-run-key")
	t.Setenv(OAuthClientIDEnvVar, "")
	t.Setenv(OAuthClientSecretEnvVar, "")
	var got Credentials
	p := &Plugin{newBackend: func(c Credentials) Backend { got = c; return &fakeBackend{} }}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.APIKey != "direct-run-key" {
		t.Fatalf("credentials = %+v", got)
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
	if spec.ID != ConnectorID || !strings.HasSuffix(spec.Entrypoint.Command, BinaryName) {
		t.Fatalf("plugin id %q entrypoint %q", spec.ID, spec.Entrypoint.Command)
	}
	if len(spec.Cerberus.Connector.Operations) != len(Definition().Operations) {
		t.Fatal("manifest operations drifted from the connector definition")
	}
}

func TestHealthReportsAConfiguredCredential(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	status, err := loadedPlugin(t, &fakeBackend{}).Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// With no tailscale binary on a bare PATH, a configured credential alone
	// keeps the plugin healthy.
	if _, cliErr := resolveTailscale(); cliErr != nil && !status.OK {
		t.Fatalf("Health = %+v", status)
	}
}
