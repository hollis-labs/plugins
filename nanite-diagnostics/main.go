package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const pluginID = "nanite.diagnostics"
const version = "0.1.0"

var resources = []pluginapi.QueryResource{pluginapi.QueryUsage, pluginapi.QueryExecutionMetrics, pluginapi.QueryContextSlots}

type diagnosticsPlugin struct {
	mu       sync.RWMutex
	client   *pluginapi.QueryClient
	lifetime context.Context
	cancel   context.CancelFunc
}

func (p *diagnosticsPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, fmt.Errorf("diagnostics: read grant required")
	}
	if grant.PluginID != pluginID || grant.Scope.IncludeContent || len(grant.Scope.Resources) != len(resources) {
		return subprocess.InitResult{}, fmt.Errorf("diagnostics: accounting-only owner grant required")
	}
	for _, resource := range resources {
		if !slices.Contains(grant.Scope.Resources, resource) {
			return subprocess.InitResult{}, fmt.Errorf("diagnostics: incomplete accounting grant")
		}
	}
	client, err := pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, fmt.Errorf("diagnostics: invalid read grant")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.client = client
	p.lifetime, p.cancel = context.WithCancel(context.Background())
	return subprocess.InitResult{ID: pluginID, Name: "Session Diagnostics", Version: version, Description: "Recorded session accounting and static prompt references", Protocol: subprocess.ProtocolVersion}, nil
}

func (*diagnosticsPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *diagnosticsPlugin) Unload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.client = nil
	return nil
}

func declaration() (manifest.Manifest, error) {
	block := pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Panels: []pluginapi.Panel{
			{ID: "session-diagnostics", Title: "Session Diagnostics", Component: "DiagnosticsPanel", Icon: "activity", Description: "Recorded usage, recent metrics and latest captured slot accounting", Order: 100},
			{ID: "system-prompt-reference", Title: "Static Prompt References", Component: "SystemPromptsViewer", Icon: "file-code", Description: "Static reference text from a pinned source revision", Order: 110},
		},
		HTTPRoutes: []pluginapi.Route{{Method: "GET", Path: "diagnostics"}},
	}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	scope, err := json.Marshal(pluginapi.QueryScope{Resources: resources, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Session Diagnostics", Version: version, Description: "Read-only recorded session diagnostics and static prompt references", License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/diagnostics"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Workspace-wide READ authority over recorded usage, execution metrics and captured slot accounting for any session in this workspace. No content and no mutations.", Metadata: scope}},
	}
	return m, m.Validate()
}

func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--manifest" {
		var m manifest.Manifest
		m, err = declaration()
		if err == nil {
			err = manifest.Encode(os.Stdout, m)
		}
	} else if len(os.Args) == 1 {
		err = subprocess.Serve(&diagnosticsPlugin{})
	} else {
		err = fmt.Errorf("usage: diagnostics [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
