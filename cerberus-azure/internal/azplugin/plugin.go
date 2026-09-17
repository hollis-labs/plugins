package azplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// backendFactory is resolved lazily on Load, after Init has handed over the
// host-resolved config, so the backend is built with the credential rather than
// before it exists.
type backendFactory func(subscriptionID string, creds Credentials) (Backend, error)

// Plugin serves the Azure connector over the plugin-sdk subprocess protocol.
type Plugin struct {
	newBackend backendFactory
	config     subprocess.ConfigReader
	backend    Backend
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against live ARM. No credential is looked up here:
// Cerberus resolves every secret this plugin's manifest declares — through
// process env, connector-secrets.yaml, then the keychain — and hands the value
// over in init config. See docs/secrets.md in the Cerberus repo.
func New() *Plugin {
	return &Plugin{newBackend: NewSDKBackend}
}

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(string, Credentials) (Backend, error) { return backend, nil },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker, so a later log line carrying the
	// client secret as a field writes REDACTED instead of the secret.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Azure Connector",
		Version:     def.Version,
		Description: "Read-only Azure inventory and AI model deployment inspection for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	if p.backend != nil {
		return subprocess.LoadResult{}, nil
	}
	backend, err := p.newBackend(p.setting(ConfigSubscriptionID, SubscriptionEnvVar), p.credentials())
	if err != nil {
		return subprocess.LoadResult{}, err
	}
	p.backend = backend
	return subprocess.LoadResult{}, nil
}

// credentials reads what the host resolved on this plugin's behalf. A missing
// client secret is not fatal: with no service principal configured the plugin
// authenticates as the signed-in Azure CLI user.
func (p *Plugin) credentials() Credentials {
	creds := Credentials{
		TenantID: p.setting(ConfigTenantID, TenantEnvVar),
		ClientID: p.setting(ConfigClientID, ClientIDEnvVar),
	}
	if p.config != nil {
		creds.ClientSecret = p.config.Secret(SecretClientSecret)
	}
	if creds.ClientSecret == "" {
		creds.ClientSecret = os.Getenv(ClientSecretEnvVar)
	}
	return creds
}

// setting prefers the host-supplied config field, then the direct-run
// environment override, for a binary run outside the host with no init config.
func (p *Plugin) setting(key, envVar string) string {
	if p.config != nil {
		if value := p.config.String(key); value != "" {
			return value
		}
	}
	return os.Getenv(envVar)
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// Health resolves the subscription operations act on. That single call proves
// the credential works, the network is reachable and the subscription exists —
// and its message names which identity answered, which is what an operator
// wants before wondering why a resource is missing.
func (p *Plugin) Health(ctx context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: "not loaded"}, nil
	}
	sub, err := p.backend.ResolveSubscription(ctx, "")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: err.Error()}, nil
	}
	return subprocess.HealthStatus{
		OK: true,
		Message: fmt.Sprintf("%s credential reads subscription %s (%s), state %s",
			p.backend.CredentialSource(), sub.DisplayName, sub.SubscriptionID, sub.State),
	}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if p.backend == nil {
		return subprocess.MCPCallResult{}, fmt.Errorf("azure plugin is not loaded")
	}

	switch req.ToolName {
	case toolName(OpListSubscriptions):
		return marshalResult(p.backend.ListSubscriptions(ctx))
	case toolName(OpGetSubscription):
		return marshalResult(p.backend.ResolveSubscription(ctx, stringArg(req, ArgSubscriptionID)))
	case toolName(OpListResourceGroups):
		return p.inSubscription(ctx, req, func(subscriptionID string) (any, error) {
			return p.backend.ListResourceGroups(ctx, subscriptionID)
		})
	case toolName(OpListResources):
		return p.inSubscription(ctx, req, func(subscriptionID string) (any, error) {
			return p.backend.ListResources(ctx, subscriptionID)
		})
	case toolName(OpListAIAccounts):
		return p.inSubscription(ctx, req, func(subscriptionID string) (any, error) {
			return p.backend.ListAIAccounts(ctx, subscriptionID)
		})
	case toolName(OpListModelDeployments):
		return p.inSubscription(ctx, req, func(subscriptionID string) (any, error) {
			return p.backend.ListModelDeployments(ctx, subscriptionID,
				stringArg(req, ArgResourceGroup), stringArg(req, ArgAccount))
		})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
}

func toolName(operation string) string {
	return cerbplugin.ToolNameForOperation(ConnectorID, operation)
}

// inSubscription resolves the subscription once, so every operation reports the
// same "which subscription, and why that one" error rather than each ARM client
// failing its own way.
func (p *Plugin) inSubscription(ctx context.Context, req subprocess.MCPCallRequest, run func(subscriptionID string) (any, error)) (subprocess.MCPCallResult, error) {
	sub, err := p.backend.ResolveSubscription(ctx, stringArg(req, ArgSubscriptionID))
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return marshalResult(run(sub.SubscriptionID))
}

// stringArg reads one operation argument. The CLI passes every `--arg key=value`
// as a string, but the HTTP and MCP lanes can send a real JSON value, so accept
// both rather than only what the CLI happens to send.
func stringArg(req subprocess.MCPCallRequest, key string) string {
	value, ok := req.Arguments[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func marshalResult[T any](data T, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
