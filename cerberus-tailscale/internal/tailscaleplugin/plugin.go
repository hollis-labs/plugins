package tailscaleplugin

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
		Name:     "Cerberus Tailscale Connector",
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
	_, err := exec.LookPath("tailscale")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "tailscale binary not found in PATH"}, nil
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
		return p.runTailscale(ctx, []string{"status"})
	case "up":
		return p.runTailscale(ctx, []string{"up"})
	case "down":
		return p.runTailscale(ctx, []string{"down"})
	case "serve":
		port, _ := args["port"].(string)
		funnel, _ := args["funnel"].(bool)
		
		if funnel {
			return p.runTailscale(ctx, []string{"funnel", "--bg", port})
		}
		return p.runTailscale(ctx, []string{"serve", "--bg", port})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runTailscale(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	cmdArgs := append([]string{"tailscale"}, args...)
	// tailscale might need sudo for `up` and `down`, but usually CLI runs them via tailscaled which runs as root.
	// We use `sudo` here for commands other than status.
	var out []byte
	var err error
	
	// 'status' rarely needs sudo
	if args[0] == "status" {
		out, err = exec.CommandContext(ctx, "tailscale", args...).CombinedOutput()
	} else {
		out, err = exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	}
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("tailscale %v failed: %v\n%s", args, err, string(out))
	} else {
		respText = fmt.Sprintf("tailscale %v successful.\n%s", args, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
