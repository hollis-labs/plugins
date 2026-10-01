package ufwplugin

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
	return subprocess.InitResult{
		ID:       ConnectorID,
		Name:     "Cerberus UFW Connector",
		Version:  Version,
		Protocol: subprocess.ProtocolVersion,
	}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	return nil
}

func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	_, err := exec.LookPath("ufw")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "ufw binary not found in PATH"}, nil
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
	case "status":
		return p.runUFW(ctx, []string{"status"})
	case "allow":
		port, _ := args["port"].(string)
		return p.runUFW(ctx, []string{"allow", port})
	case "deny":
		port, _ := args["port"].(string)
		return p.runUFW(ctx, []string{"deny", port})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runUFW(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	// Need sudo for ufw, we assume Cerberus is run with sufficient privileges or sudo without password is setup.
	cmdArgs := append([]string{"ufw"}, args...)
	out, err := exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("ufw %v failed: %v\n%s", args, err, string(out))
	} else {
		respText = fmt.Sprintf("ufw %v successful.\n%s", args, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
