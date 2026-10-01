package resticplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		Name:     "Cerberus Restic Connector",
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
	_, err := exec.LookPath("restic")
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: "restic binary not found in PATH"}, nil
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
	case "init":
		repo, _ := args["repo"].(string)
		return p.runRestic(ctx, []string{"init", "-r", repo})
	case "backup":
		repo, _ := args["repo"].(string)
		path, _ := args["path"].(string)
		return p.runRestic(ctx, []string{"backup", "-r", repo, path})
	case "snapshots":
		repo, _ := args["repo"].(string)
		return p.runRestic(ctx, []string{"snapshots", "-r", repo})
	case "restore":
		repo, _ := args["repo"].(string)
		snapshot, _ := args["snapshot"].(string)
		target, _ := args["target"].(string)
		return p.runRestic(ctx, []string{"restore", "-r", repo, snapshot, "--target", target})
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
}

func (p *Plugin) runRestic(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	cmd := exec.CommandContext(ctx, "restic", args...)
	
	// Inject password if provided in config
	if pwd, ok := p.config["RESTIC_PASSWORD"]; ok && pwd != "" {
		cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+pwd)
	}

	out, err := cmd.CombinedOutput()
	
	respText := string(out)
	if err != nil {
		respText = fmt.Sprintf("restic %v failed: %v\n%s", args, err, string(out))
	}
	
	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
