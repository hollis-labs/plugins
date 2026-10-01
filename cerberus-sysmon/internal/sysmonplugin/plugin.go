package sysmonplugin

import (
	"context"
	"encoding/json"
	"fmt"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
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
		Name:     "Cerberus Sysmon Connector",
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
		result, err = getCPU()
	case "memory":
		result, err = getMemory()
	case "disk":
		path, _ := args["path"].(string)
		if path == "" {
			path = "/"
		}
		result, err = getDisk(path)
	case "host":
		result, err = getHost()
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}

	if err != nil {
		return subprocess.MCPCallResult{}, err
	}

	content, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	
	// returning as a JSON string to match schema
	strContent, _ := json.Marshal(string(content))

	return subprocess.MCPCallResult{Content: strContent}, nil
}

func getCPU() (any, error) {
	percentages, err := cpu.Percent(0, false)
	if err != nil {
		return nil, err
	}
	info, err := cpu.Info()
	if err != nil {
		return nil, err
	}
	
	return map[string]any{
		"usage_percent": percentages,
		"info":          info,
	}, nil
}

func getMemory() (any, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return nil, err
	}
	return v, nil
}

func getDisk(path string) (any, error) {
	d, err := disk.Usage(path)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func getHost() (any, error) {
	h, err := host.Info()
	if err != nil {
		return nil, err
	}
	return h, nil
}
