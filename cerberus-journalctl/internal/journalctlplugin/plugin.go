package journalctlplugin

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
		Name:     "Cerberus Journalctl Connector",
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
	_, err := exec.LookPath("journalctl")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "journalctl binary not found in PATH"}, nil
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
	case "logs":
		unit, _ := args["unit"].(string)
		linesFloat, ok := args["lines"].(float64)
		lines := "100"
		if ok {
			lines = fmt.Sprintf("%d", int(linesFloat))
		}
		since, _ := args["since"].(string)

		cmdArgs := []string{"-u", unit, "-n", lines, "--no-pager"}
		if since != "" {
			cmdArgs = append(cmdArgs, "--since", since)
		}
		return p.runJournal(ctx, cmdArgs)

	case "system_logs":
		linesFloat, ok := args["lines"].(float64)
		lines := "100"
		if ok {
			lines = fmt.Sprintf("%d", int(linesFloat))
		}
		since, _ := args["since"].(string)
		grepStr, _ := args["grep"].(string)

		cmdArgs := []string{"-n", lines, "--no-pager"}
		if since != "" {
			cmdArgs = append(cmdArgs, "--since", since)
		}
		if grepStr != "" {
			cmdArgs = append(cmdArgs, "--grep", grepStr)
		}
		return p.runJournal(ctx, cmdArgs)

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runJournal(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	// journalctl might need sudo depending on the system config (e.g. for system logs)
	cmdArgs := append([]string{"journalctl"}, args...)
	out, err := exec.CommandContext(ctx, "sudo", cmdArgs...).CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("journalctl %v failed: %v\n%s", args, err, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
