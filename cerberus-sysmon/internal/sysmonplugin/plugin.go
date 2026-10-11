package sysmonplugin

import (
	"context"
	"encoding/json"
	"fmt"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

type Plugin struct {
	config map[string]string
	env    Env
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

func New() *Plugin { return &Plugin{env: RealEnv()} }

// NewWithEnv serves from a substituted machine; tests use it.
func NewWithEnv(e Env) *Plugin { return &Plugin{env: e} }

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.config = params.Config
	return subprocess.InitResult{
		ID:                 ConnectorID,
		Name:               "Cerberus Sysmon Connector",
		Version:            Version,
		Protocol:           subprocess.ProtocolVersion,
		CapabilityContract: capability.ContractVersion,
	}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	return nil
}

func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	return subprocess.HealthStatus{OK: true}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
	}

	args := req.Arguments

	var result any
	var err error

	switch op.Name {
	case "cpu":
		result, err = CollectCPU(ctx, p.env)
	case "memory":
		result, err = CollectMemory(ctx, p.env)
	case "disk":
		path, _ := args["path"].(string)
		if path == "" {
			path = "/"
		}
		result, err = CollectDisk(ctx, p.env, path)
	case "host":
		result, err = CollectHost(ctx, p.env)
	case "host_report":
		result, err = CollectReport(ctx, p.env)
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}

	content, err := json.Marshal(result)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
