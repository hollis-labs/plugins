package cfplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type fakeBackend struct {
	gateways []Gateway
	servers  []VirtualServer
	tools    []Tool
	health   Health
	err      error
}

func (f *fakeBackend) ListGateways(context.Context) ([]Gateway, error) {
	return f.gateways, f.err
}
func (f *fakeBackend) ListVirtualServers(context.Context) ([]VirtualServer, error) {
	return f.servers, f.err
}
func (f *fakeBackend) ListTools(context.Context) ([]Tool, error) { return f.tools, f.err }
func (f *fakeBackend) GetHealth(context.Context) (Health, error) { return f.health, f.err }

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func callTool(t *testing.T, p *Plugin, operation string) subprocess.MCPCallResult {
	t.Helper()
	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, operation),
	})
	if err != nil {
		t.Fatalf("MCPCallTool(%s): %v", operation, err)
	}
	return result
}

// Every operation the manifest declares must be routable by the tool name the
// host derives from it. This is the contract between the two halves.
func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for _, op := range Definition().Operations {
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
			ToolName: cerbplugin.ToolNameForOperation(ConnectorID, op.Name),
		})
		if err != nil {
			t.Fatalf("operation %q is declared but not served: %v", op.Name, err)
		}
	}
}

func TestListGatewaysReturnsDTOs(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{gateways: []Gateway{{
		Name: "mcp-svc-a", URL: "http://svc-a-mcp:8000/mcp",
		AuthType: "bearer", AuthConfigured: true, Enabled: true,
	}}})

	var got []Gateway
	if err := json.Unmarshal(callTool(t, p, "list_gateways").Content, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Name != "mcp-svc-a" {
		t.Fatalf("gateways = %+v", got)
	}
	if !got[0].AuthConfigured || got[0].AuthType != "bearer" {
		t.Fatalf("auth shape lost in transit: %+v", got[0])
	}
}

func TestGetHealthReportsAddress(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{health: Health{OK: true, Status: "ok", Address: DefaultAddress}})

	var got Health
	if err := json.Unmarshal(callTool(t, p, "get_health").Content, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !got.OK || got.Address != DefaultAddress {
		t.Fatalf("health = %+v", got)
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "delete_everything"),
	})
	if err == nil {
		t.Fatal("expected an error for an undeclared tool")
	}
}

func TestCallBeforeLoadIsRejected(t *testing.T) {
	p := NewWithBackend(&fakeBackend{})
	_ = p.Unload(context.Background())
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "get_health"),
	}); err == nil {
		t.Fatal("expected an error when the plugin is not loaded")
	}
}

func TestBackendErrorsPropagate(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{err: fmt.Errorf("401 unauthorized")})
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "list_gateways"),
	}); err == nil {
		t.Fatal("expected the backend error to reach the caller")
	}
}

func TestHealthCheckReportsUnreachableGateway(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{err: fmt.Errorf("connection refused")})
	status, err := p.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if status.OK {
		t.Fatal("Health.OK = true for an unreachable gateway")
	}
}

func TestInitAdvertisesTheManifestIdentity(t *testing.T) {
	result, err := New().Init(context.Background(), subprocess.InitParams{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.ID != ConnectorID || result.Version != Version {
		t.Fatalf("InitResult = %+v", result)
	}
	if result.Protocol != subprocess.ProtocolVersion {
		t.Fatalf("Protocol = %d, want %d", result.Protocol, subprocess.ProtocolVersion)
	}
}

// The generated plugin.yaml is what the host installs, so an invalid one is a
// release-blocking bug rather than a runtime surprise.
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

// A collision with a built-in connector id is the shadowing class of bug WP-0
// fixed; contextforge must not reintroduce it.
func TestConnectorIDDoesNotCollideWithABuiltIn(t *testing.T) {
	for _, builtin := range []string{"local", "ssh", "docker", "github"} {
		if ConnectorID == builtin {
			t.Fatalf("connector id %q collides with a built-in", ConnectorID)
		}
	}
}

// The credential arrives from the host over init config. Before Cerberus grew a
// plugin secret channel this plugin read the keychain itself, which meant every
// plugin reimplemented secret resolution and connector-secrets.yaml never
// reached one.
func TestTokenComesFromHostInitConfig(t *testing.T) {
	var got struct {
		address string
		token   string
	}
	p := &Plugin{newBackend: func(address, token string) (Backend, error) {
		got.address, got.token = address, token
		return &fakeBackend{}, nil
	}}

	if _, err := p.Init(context.Background(), subprocess.InitParams{
		Config: map[string]string{SecretToken: "host-resolved-jwt"},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.token != "host-resolved-jwt" {
		t.Fatalf("token = %q, want the value the host resolved", got.token)
	}
	if got.address != DefaultAddress {
		t.Fatalf("address = %q, want the tunnel default", got.address)
	}
}

// The secret key the plugin reads must be the one its manifest declares —
// that name is what the host resolves and keys the init config by.
func TestDeclaredSecretMatchesTheKeyRead(t *testing.T) {
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 {
		t.Fatalf("Secrets = %+v, want exactly the token", secrets)
	}
	if secrets[0].Name != SecretToken {
		t.Fatalf("declared secret %q does not match the key the plugin reads, %q", secrets[0].Name, SecretToken)
	}
	if !secrets[0].Required {
		t.Fatal("the token is required; declaring it optional would hide a missing credential from the host")
	}
}

// A credential the host could not resolve must not fail the load: get_health is
// open, and is how an operator tells a down tunnel from a down gateway.
func TestLoadSucceedsWithoutAToken(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	var got string
	p := &Plugin{newBackend: func(_, token string) (Backend, error) {
		got = token
		return &fakeBackend{health: Health{OK: true}}, nil
	}}

	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load without a token: %v", err)
	}
	if got != "" {
		t.Fatalf("token = %q, want empty when the host resolved none", got)
	}
	if status, err := p.Health(context.Background()); err != nil || !status.OK {
		t.Fatalf("Health = %+v, %v; the open endpoint must still work", status, err)
	}
}

// The binary is also runnable outside the host, where there is no init config.
func TestDirectRunFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv(TokenEnvVar, "direct-run-jwt")
	var got string
	p := &Plugin{newBackend: func(_, token string) (Backend, error) {
		got = token
		return &fakeBackend{}, nil
	}}

	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "direct-run-jwt" {
		t.Fatalf("token = %q, want the direct-run fallback", got)
	}
}
