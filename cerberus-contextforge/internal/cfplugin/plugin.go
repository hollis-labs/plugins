package cfplugin

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
type backendFactory func(address, token string) (Backend, error)

// Plugin serves the ContextForge connector over the plugin-sdk subprocess
// protocol.
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

// New builds the plugin against the live gateway. The JWT is not looked up
// here: Cerberus resolves every secret this plugin's manifest declares — through
// process env, connector-secrets.yaml, then the keychain — and hands the value
// over in init config. See docs/secrets.md in the Cerberus repo.
func New() *Plugin {
	return &Plugin{newBackend: NewSDKBackend}
}

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(string, string) (Backend, error) { return backend, nil },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker, so a later log line carrying the
	// token as a field writes REDACTED instead of the JWT.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus ContextForge Connector",
		Version:     def.Version,
		Description: "ContextForge MCP gateway administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	if p.backend != nil {
		return subprocess.LoadResult{}, nil
	}
	// A missing token is not fatal: get_health is open and is how you tell a
	// down tunnel from a down gateway, and the auth'd operations report a 401
	// that names how to supply one.
	backend, err := p.newBackend(p.address(), p.token())
	if err != nil {
		return subprocess.LoadResult{}, err
	}
	p.backend = backend
	return subprocess.LoadResult{}, nil
}

// token reads the JWT the host resolved on this plugin's behalf, falling back
// to the environment only for a binary run directly, outside the host, which
// receives no init config.
func (p *Plugin) token() string {
	if p.config != nil {
		if token := p.config.Secret(SecretToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

// address prefers a host-supplied config field, then the direct-run override,
// then the tunnel default.
func (p *Plugin) address() string {
	if p.config != nil {
		if address := p.config.String(ConfigAddress); address != "" {
			return address
		}
	}
	return ResolveAddress()
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

func (p *Plugin) Health(ctx context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: "not loaded"}, nil
	}
	health, err := p.backend.GetHealth(ctx)
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: err.Error()}, nil
	}
	if !health.OK {
		return subprocess.HealthStatus{OK: false, Message: "gateway " + health.Status}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "gateway reachable at " + health.Address}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if p.backend == nil {
		return subprocess.MCPCallResult{}, fmt.Errorf("contextforge plugin is not loaded")
	}

	switch req.ToolName {
	case cerbplugin.ToolNameForOperation(ConnectorID, "get_health"):
		return marshalResult(p.backend.GetHealth(ctx))
	case cerbplugin.ToolNameForOperation(ConnectorID, "list_gateways"):
		return marshalResult(p.backend.ListGateways(ctx))
	case cerbplugin.ToolNameForOperation(ConnectorID, "list_virtual_servers"):
		return marshalResult(p.backend.ListVirtualServers(ctx))
	case cerbplugin.ToolNameForOperation(ConnectorID, "list_tools"):
		return marshalResult(p.backend.ListTools(ctx))
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
}

func marshalResult[T any](data T, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		// A coded failure travels as a tool error result, so the host
		// reports the plugin's code rather than guessing one.
		if result, ok := cerbplugin.ResultForError(err); ok {
			return result, nil
		}
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
