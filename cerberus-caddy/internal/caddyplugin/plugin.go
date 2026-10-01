package caddyplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type Plugin struct {
	config map[string]string
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.config = params.Config
	return subprocess.InitResult{ID: ConnectorID, Name: "Cerberus Caddy Connector", Version: Version, Protocol: subprocess.ProtocolVersion}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	return nil
}

func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	_, err := exec.LookPath("caddy")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "caddy binary not found in PATH"}, nil
	}
	return subprocess.HealthStatus{OK: true}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
	}

	args := req.Arguments

	switch op.Name {
	case "reload":
		return p.reload(ctx, args)
	case "validate":
		return p.validate(ctx, args)
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) reload(ctx context.Context, args map[string]any) (subprocess.MCPCallResult, error) {
	cmdArgs := []string{"reload"}
	if cp, ok := args["config_path"].(string); ok && cp != "" {
		cmdArgs = append(cmdArgs, "--config", cp)
	}

	out, err := exec.CommandContext(ctx, "caddy", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("caddy reload failed: %v\n%s", err, string(out))
	} else {
		respText = "Caddy reloaded successfully.\n" + string(out)
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}

func (p *Plugin) validate(ctx context.Context, args map[string]any) (subprocess.MCPCallResult, error) {
	cmdArgs := []string{"validate"}
	if cp, ok := args["config_path"].(string); ok && cp != "" {
		cmdArgs = append(cmdArgs, "--config", cp)
	}

	out, err := exec.CommandContext(ctx, "caddy", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("caddy validate failed: %v\n%s", err, string(out))
	} else {
		respText = "Caddyfile is valid.\n" + string(out)
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
