package fail2banplugin

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
		Name:     "Cerberus Fail2ban Connector",
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
	_, err := exec.LookPath("fail2ban-client")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "fail2ban-client binary not found in PATH"}, nil
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
		jail, _ := args["jail"].(string)
		cmdArgs := []string{"status"}
		if jail != "" {
			cmdArgs = append(cmdArgs, jail)
		}
		return p.runFail2ban(ctx, cmdArgs)

	case "unban":
		jail, _ := args["jail"].(string)
		ip, _ := args["ip"].(string)
		return p.runFail2ban(ctx, []string{"set", jail, "unbanip", ip})

	case "ban":
		jail, _ := args["jail"].(string)
		ip, _ := args["ip"].(string)
		return p.runFail2ban(ctx, []string{"set", jail, "banip", ip})

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runFail2ban(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	// fail2ban-client requires sudo for most operations (especially set)
	cmdArgs := append([]string{"fail2ban-client"}, args...)
	out, err := exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("fail2ban-client %v failed: %v\n%s", args, err, string(out))
	} else {
		respText = fmt.Sprintf("fail2ban-client %v successful.\n%s", args, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
