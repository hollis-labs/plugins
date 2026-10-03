package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("PLAN_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&planPlugin{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
func TestSDKProcessReconnect(t *testing.T) {
	var params subprocess.InitParams
	_, receipts := pluginFixture(t, true, &params)
	run := func(create bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKHelperProcess$")
		child.Env = append(os.Environ(), "PLAN_TEST_CHILD=1", "GORACE=atexit_sleep_ms=0", "HOME="+t.TempDir())
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
				t.Fatal("SDK stopped", scanner.Err(), diagnostics.String())
			}
			var response struct {
				Result json.RawMessage
				Error  any
			}
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error != nil {
				t.Fatal(method, string(scanner.Bytes()))
			}
			return response.Result
		}
		call("plugin/init", params)
		call("plugin/load", map[string]any{})
		if create {
			raw := call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "todo_create", SessionID: "session-a", Arguments: map[string]any{"title": "Wire work"}})
			var result subprocess.MCPCallResult
			if err = json.Unmarshal(raw, &result); err != nil || result.IsError {
				t.Fatal(string(raw), err)
			}
		}
		raw := call("http/handle", subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/todos", RawQuery: "session_id=session-a"})
		var response subprocess.HTTPResponse
		if err = json.Unmarshal(raw, &response); err != nil || response.Status != 200 || !bytes.Contains(response.Body, []byte("Wire work")) {
			t.Fatal("SDK reconnect lost work", string(raw), err)
		}
		call("plugin/unload", map[string]any{})
		if err = input.Close(); err != nil {
			t.Fatal(err)
		}
		if err = child.Wait(); err != nil {
			t.Fatal("SDK exit", err, diagnostics.String())
		}
	}
	run(true)
	for _, receipt := range receipts {
		if err := os.Remove(params.DataDir + "/" + receipt.Path); err != nil {
			t.Fatal(err)
		}
	}
	run(false)
}
