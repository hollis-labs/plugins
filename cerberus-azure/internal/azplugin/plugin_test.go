package azplugin

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
	subscriptions []Subscription
	resolved      Subscription
	groups        []ResourceGroup
	resources     []Resource
	accounts      []AIAccount
	deployments   []ModelDeployment
	source        string
	err           error

	// calls records what each operation asked for, so argument plumbing is
	// asserted rather than assumed.
	calls struct {
		resolveRequested string
		subscriptionID   string
		resourceGroup    string
		account          string
	}
}

func (f *fakeBackend) ResolveSubscription(_ context.Context, requested string) (Subscription, error) {
	f.calls.resolveRequested = requested
	if f.err != nil {
		return Subscription{}, f.err
	}
	if requested != "" {
		return Subscription{SubscriptionID: requested}, nil
	}
	return f.resolved, nil
}

func (f *fakeBackend) ListSubscriptions(context.Context) ([]Subscription, error) {
	return f.subscriptions, f.err
}

func (f *fakeBackend) ListResourceGroups(_ context.Context, subscriptionID string) ([]ResourceGroup, error) {
	f.calls.subscriptionID = subscriptionID
	return f.groups, f.err
}

func (f *fakeBackend) ListResources(_ context.Context, subscriptionID string) ([]Resource, error) {
	f.calls.subscriptionID = subscriptionID
	return f.resources, f.err
}

func (f *fakeBackend) ListAIAccounts(_ context.Context, subscriptionID string) ([]AIAccount, error) {
	f.calls.subscriptionID = subscriptionID
	return f.accounts, f.err
}

func (f *fakeBackend) ListModelDeployments(_ context.Context, subscriptionID, resourceGroup, account string) ([]ModelDeployment, error) {
	f.calls.subscriptionID = subscriptionID
	f.calls.resourceGroup = resourceGroup
	f.calls.account = account
	return f.deployments, f.err
}

func (f *fakeBackend) CredentialSource() string {
	if f.source == "" {
		return SourceAzureCLI
	}
	return f.source
}

var _ Backend = (*fakeBackend)(nil)

func defaultSubscription() Subscription {
	return Subscription{
		SubscriptionID: "00000000-0000-0000-0000-000000000000",
		DisplayName:    "example-subscription",
		State:          "Enabled",
		TenantID:       "423946e4-28c0-4deb-904c-a4a4b174fb3f",
	}
}

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func callTool(t *testing.T, p *Plugin, operation string, args map[string]any) subprocess.MCPCallResult {
	t.Helper()
	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("MCPCallTool(%s): %v", operation, err)
	}
	return result
}

// Every operation the manifest declares must be routable by the tool name the
// host derives from it. This is the contract between the two halves.
func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{resolved: defaultSubscription()})
	for _, op := range Definition().Operations {
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
			ToolName: cerbplugin.ToolNameForOperation(ConnectorID, op.Name),
		})
		if err != nil {
			t.Fatalf("operation %q is declared but not served: %v", op.Name, err)
		}
	}
}

// This connector's whole scope is read and probe. Cerberus does not own the
// Azure estate, and a write operation appearing here should fail a test rather
// than a review.
func TestNoOperationIsDestructive(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.Destructive || op.SupportsDry {
			t.Fatalf("operation %q is not read-only: destructive=%v supports_dry=%v", op.Name, op.Destructive, op.SupportsDry)
		}
	}
}

// The acceptance case for WP-5, against a fake: the zero-argument call answers
// "what models can we call, at what version".
func TestListModelDeploymentsReturnsDTOs(t *testing.T) {
	backend := &fakeBackend{
		resolved: defaultSubscription(),
		deployments: []ModelDeployment{{
			Name: "claude-sonnet-5", Account: "Example-AI-Account",
			ResourceGroup: "rg-example-ai", Model: "claude-sonnet-5",
			ModelVersion: "2", SKU: "GlobalStandard", Capacity: 5000,
		}},
	}
	p := loadedPlugin(t, backend)

	var got []ModelDeployment
	if err := json.Unmarshal(callTool(t, p, OpListModelDeployments, nil).Content, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Name != "claude-sonnet-5" || got[0].ModelVersion != "2" {
		t.Fatalf("deployments = %+v", got)
	}
	if backend.calls.subscriptionID != defaultSubscription().SubscriptionID {
		t.Fatalf("subscription = %q, want the resolved default", backend.calls.subscriptionID)
	}
}

// The CLI sends every `--arg key=value` as a string; the HTTP and MCP lanes can
// send a real JSON value. Both must reach the backend.
func TestOperationArgumentsReachTheBackend(t *testing.T) {
	backend := &fakeBackend{resolved: defaultSubscription()}
	p := loadedPlugin(t, backend)

	callTool(t, p, OpListModelDeployments, map[string]any{
		ArgAccount:        "Example-AI-Account",
		ArgResourceGroup:  "rg-example-ai",
		ArgSubscriptionID: "sub-override",
	})

	if backend.calls.account != "Example-AI-Account" {
		t.Fatalf("account = %q", backend.calls.account)
	}
	if backend.calls.resourceGroup != "rg-example-ai" {
		t.Fatalf("resource group = %q", backend.calls.resourceGroup)
	}
	if backend.calls.subscriptionID != "sub-override" {
		t.Fatalf("subscription = %q, want the explicit argument to win", backend.calls.subscriptionID)
	}
}

func TestNonStringArgumentsAreAccepted(t *testing.T) {
	backend := &fakeBackend{resolved: defaultSubscription()}
	p := loadedPlugin(t, backend)
	callTool(t, p, OpListModelDeployments, map[string]any{ArgAccount: 42})
	if backend.calls.account != "42" {
		t.Fatalf("account = %q, want a non-string argument coerced", backend.calls.account)
	}
}

func TestMissingArgumentsAreEmptyNotNil(t *testing.T) {
	backend := &fakeBackend{resolved: defaultSubscription()}
	p := loadedPlugin(t, backend)
	callTool(t, p, OpListModelDeployments, map[string]any{ArgAccount: nil})
	if backend.calls.account != "" {
		t.Fatalf("account = %q, want empty for a nil argument", backend.calls.account)
	}
}

func TestGetSubscriptionResolvesTheDefault(t *testing.T) {
	backend := &fakeBackend{resolved: defaultSubscription()}
	p := loadedPlugin(t, backend)

	var got Subscription
	if err := json.Unmarshal(callTool(t, p, OpGetSubscription, nil).Content, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.SubscriptionID != defaultSubscription().SubscriptionID || got.State != "Enabled" {
		t.Fatalf("subscription = %+v", got)
	}
}

func TestListSubscriptionsDoesNotResolveADefault(t *testing.T) {
	backend := &fakeBackend{subscriptions: []Subscription{defaultSubscription()}}
	p := loadedPlugin(t, backend)

	var got []Subscription
	if err := json.Unmarshal(callTool(t, p, OpListSubscriptions, nil).Content, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("subscriptions = %+v", got)
	}
	if backend.calls.resolveRequested != "" {
		t.Fatal("list_subscriptions resolved a default subscription; it must work before one is chosen")
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "delete_vm"),
	})
	if err == nil {
		t.Fatal("expected an error for an undeclared tool")
	}
}

func TestCallBeforeLoadIsRejected(t *testing.T) {
	p := NewWithBackend(&fakeBackend{})
	_ = p.Unload(context.Background())
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, OpListSubscriptions),
	}); err == nil {
		t.Fatal("expected an error when the plugin is not loaded")
	}
}

func TestBackendErrorsPropagate(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{err: fmt.Errorf("403 AuthorizationFailed")})
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, OpListResources),
	}); err == nil {
		t.Fatal("expected the backend error to reach the caller")
	}
}

// The health message names the identity and the subscription, because "why is
// this resource missing" is nearly always one of those two being unexpected.
func TestHealthNamesTheIdentityAndSubscription(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{resolved: defaultSubscription(), source: SourceServicePrincipal})
	status, err := p.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !status.OK {
		t.Fatalf("Health.OK = false: %s", status.Message)
	}
	for _, want := range []string{SourceServicePrincipal, "example-subscription", defaultSubscription().SubscriptionID} {
		if !strings.Contains(status.Message, want) {
			t.Fatalf("health message does not name %q: %s", want, status.Message)
		}
	}
}

func TestHealthReportsAnUnreachableSubscription(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{err: fmt.Errorf("no subscription is visible")})
	status, err := p.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if status.OK {
		t.Fatal("Health.OK = true when the subscription could not be resolved")
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
// fixed, and the host refuses such a plugin at install.
func TestConnectorIDDoesNotCollideWithABuiltIn(t *testing.T) {
	for _, builtin := range []string{"local", "ssh", "docker", "github"} {
		if ConnectorID == builtin {
			t.Fatalf("connector id %q collides with a built-in", ConnectorID)
		}
	}
}

// The credential arrives from the host over init config, never from the
// environment: the host's plugin launch env is an allow-list carrying no
// credentials, precisely so a secret reaches only the plugin that declared it.
func TestCredentialsComeFromHostInitConfig(t *testing.T) {
	var got struct {
		subscriptionID string
		creds          Credentials
	}
	p := &Plugin{newBackend: func(subscriptionID string, creds Credentials) (Backend, error) {
		got.subscriptionID, got.creds = subscriptionID, creds
		return &fakeBackend{}, nil
	}}

	if _, err := p.Init(context.Background(), subprocess.InitParams{
		Config: map[string]string{
			ConfigSubscriptionID: "sub-from-host",
			ConfigTenantID:       "tenant-from-host",
			ConfigClientID:       "client-from-host",
			SecretClientSecret:   "host-resolved-secret",
		},
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.subscriptionID != "sub-from-host" {
		t.Fatalf("subscription = %q, want the host-resolved value", got.subscriptionID)
	}
	if got.creds.ClientSecret != "host-resolved-secret" || !got.creds.ServicePrincipal() {
		t.Fatalf("credentials = %+v, want the host-resolved service principal", got.creds)
	}
}

// A missing credential is not fatal. The plugin loads and authenticates as the
// signed-in Azure CLI user, which is the path this connector was verified on.
func TestLoadSucceedsWithNoCredential(t *testing.T) {
	p := &Plugin{newBackend: func(string, Credentials) (Backend, error) { return &fakeBackend{}, nil }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load with no credential: %v", err)
	}
}

// Every declared secret and config field must be one the plugin actually reads.
// A manifest entry nothing consumes is a promise the host cannot keep.
func TestManifestDeclarationsAreConsumed(t *testing.T) {
	declared := map[string]bool{}
	for _, field := range Definition().Config.Fields {
		declared[field.Name] = true
	}
	for _, secret := range Definition().Config.Secrets {
		declared[secret.Name] = true
	}
	for _, key := range []string{ConfigSubscriptionID, ConfigTenantID, ConfigClientID, SecretClientSecret} {
		if !declared[key] {
			t.Fatalf("the plugin reads %q but the manifest does not declare it", key)
		}
		delete(declared, key)
	}
	for key := range declared {
		t.Fatalf("the manifest declares %q but nothing reads it", key)
	}
}
