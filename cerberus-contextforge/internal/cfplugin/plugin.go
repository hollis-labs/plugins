package cfplugin

import (
	"context"
	"encoding/json"
	"fmt"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// backendFactory is resolved lazily on Load so that construction failures — a
// missing token most of all — surface as a load error rather than at init.
type backendFactory func() (Backend, error)

// Plugin serves the ContextForge connector over the plugin-sdk subprocess
// protocol.
type Plugin struct {
	newBackend backendFactory
	backend    Backend
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live gateway, resolving the address and the
// JWT from the environment and the secret store at Load time.
func New() *Plugin {
	return &Plugin{newBackend: func() (Backend, error) {
		address := ResolveAddress()
		// A missing token is not fatal: get_health is open, and the auth'd
		// operations report a clear 401 that names how to supply one.
		token, _ := ResolveToken()
		return NewSDKBackend(address, token)
	}}
}

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func() (Backend, error) { return backend, nil },
		backend:    backend,
	}
}

func (p *Plugin) Init(context.Context, subprocess.InitParams) (subprocess.InitResult, error) {
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
	backend, err := p.newBackend()
	if err != nil {
		return subprocess.LoadResult{}, err
	}
	p.backend = backend
	return subprocess.LoadResult{}, nil
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
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
