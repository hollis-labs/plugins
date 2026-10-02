package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("MEDIA_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&giphyPlugin{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
func TestActualSDKProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKHelperProcess$")
	child.Env = append(os.Environ(), "MEDIA_TEST_CHILD=1", "GORACE=atexit_sleep_ms=0")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	child.Stderr = &diagnostics
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	encoder := json.NewEncoder(input)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	id := 0
	call := func(method string, args any) json.RawMessage {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": args}); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() {
			t.Fatalf("SDK child stopped: %v %s", scanner.Err(), diagnostics.String())
		}
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Error != nil {
			t.Fatalf("RPC %s failed: %s", method, scanner.Bytes())
		}
		return response.Result
	}
	call("plugin/init", subprocess.InitParams{})
	call("plugin/load", map[string]any{})
	var command subprocess.CommandResult
	if err = json.Unmarshal(call("command/execute", subprocess.CommandExecParams{Name: "giphy", Args: "cats", SessionID: "session-one"}), &command); err != nil {
		t.Fatal(err)
	}
	if len(command.Envelopes) != 1 || command.Envelopes[0].Type != envelopeType {
		t.Fatalf("wire command: %#v", command)
	}
	var tool subprocess.MCPCallResult
	if err = json.Unmarshal(call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "giphy_search", SessionID: "session-one", Arguments: map[string]any{"query": "cats"}}), &tool); err != nil {
		t.Fatal(err)
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err = json.Unmarshal(tool.Content, &content); err != nil {
		t.Fatal(err)
	}
	if tool.IsError || len(content) != 1 || content[0].Type != "text" || !json.Valid([]byte(content[0].Text)) || len(tool.Envelopes) != 1 {
		t.Fatalf("wire tool: %#v", tool)
	}
	call("plugin/unload", map[string]any{})
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatalf("SDK exit: %v %s", err, diagnostics.String())
	}
}
