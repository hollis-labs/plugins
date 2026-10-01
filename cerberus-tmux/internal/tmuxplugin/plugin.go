package tmuxplugin

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
		Name:     "Cerberus Tmux Connector",
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
	_, err := exec.LookPath("tmux")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "tmux binary not found in PATH"}, nil
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
	case "list":
		return p.runTmux(ctx, []string{"ls"})
	case "new":
		sessionName, _ := args["session_name"].(string)
		cmd, _ := args["command"].(string)
		tmuxArgs := []string{"new-session", "-d", "-s", sessionName}
		if cmd != "" {
			tmuxArgs = append(tmuxArgs, cmd)
		}
		return p.runTmux(ctx, tmuxArgs)
	case "capture":
		sessionName, _ := args["session_name"].(string)
		return p.runTmux(ctx, []string{"capture-pane", "-p", "-t", sessionName})
	case "kill":
		sessionName, _ := args["session_name"].(string)
		return p.runTmux(ctx, []string{"kill-session", "-t", sessionName})
	case "send_keys":
		sessionName, _ := args["session_name"].(string)
		keys, _ := args["keys"].(string)
		return p.runTmux(ctx, []string{"send-keys", "-t", sessionName, keys})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runTmux(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	out, err := exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("tmux %v failed: %v\n%s", args, err, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
