package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestRuntimeIdentityMatchesGeneratedDeclaration(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginapi.DecodeBlock(m.Nanite); err != nil {
		t.Fatal(err)
	}
	init, err := (example{}).Init(context.Background(), subprocess.InitParams{})
	if err != nil {
		t.Fatal(err)
	}
	if init.ID != m.ID || init.Version != m.Version || init.Protocol != m.Protocol {
		t.Fatal("runtime and declaration disagree")
	}
	for _, tool := range m.Tools {
		result, err := (example{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool.Name})
		if err != nil || result.IsError || !json.Valid(result.Content) {
			t.Fatalf("declared tool unavailable: %v", err)
		}
	}
	if _, err := (example{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "undeclared"}); err == nil {
		t.Fatal("accepted undeclared tool")
	}
}
