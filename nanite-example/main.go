// Example is a read-only subprocess plugin demonstrating the public contracts.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID = "nanite.example"
	version  = "0.1.0"
)

type example struct{}

func (example) Init(_ context.Context, _ subprocess.InitParams) (subprocess.InitResult, error) {
	return subprocess.InitResult{ID: pluginID, Name: "Example", Description: "Public plugin contract example", Version: version, Protocol: subprocess.ProtocolVersion}, nil
}
func (example) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (example) Unload(context.Context) error { return nil }
func (example) MCPCallTool(_ context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if req.ToolName != "example_hello" {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
	}
	return subprocess.MCPCallResult{Content: json.RawMessage(`[{"type":"text","text":"Hello from the Nanite example plugin"}]`)}, nil
}

func declaration() (manifest.Manifest, error) {
	block, err := pluginapi.EncodeBlock(pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Panels: []pluginapi.Panel{{ID: "example", Title: "Example", Component: "ExamplePanel"}},
	}})
	if err != nil {
		return manifest.Manifest{}, err
	}
	return manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Example", Description: "Public plugin contract example", Version: version, License: "MIT",
		Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime,
		Entrypoint: manifest.Entrypoint{Command: "bin/example"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}},
		Tools: []manifest.Tool{{Name: "example_hello", Description: "Return a greeting", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Effect: "read"}}, Nanite: block}, nil
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
		err = subprocess.Serve(example{})
	} else {
		err = fmt.Errorf("usage: example [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
