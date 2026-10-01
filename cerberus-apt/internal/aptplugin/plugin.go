package aptplugin

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
		Name:     "Cerberus APT Connector",
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
	_, err := exec.LookPath("apt-get")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "apt-get binary not found in PATH"}, nil
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
	case "update":
		return p.runAPT(ctx, []string{"update", "-y"})
	case "upgrade":
		return p.runAPT(ctx, []string{"upgrade", "-y"})
	case "install":
		pkg, _ := args["package"].(string)
		return p.runAPT(ctx, []string{"install", "-y", pkg})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runAPT(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	cmdArgs := append([]string{"apt-get"}, args...)
	out, err := exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("apt-get %v failed: %v\n%s", args, err, string(out))
	} else {
		respText = fmt.Sprintf("apt-get %v successful.\n%s", args, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
