package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("MEDIA_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&loomPlugin{}); err != nil {
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

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var wake pluginapi.DurableWakeRequest
		if err := json.NewDecoder(r.Body).Decode(&wake); err != nil || wake.AgentSlug != "loom-curator" || wake.Facts["fragment_id"] != "wire-fragment" || !strings.Contains(wake.Prompt, "fragment_id: wire-fragment") {
			t.Error("wire callback did not deliver identity")
		}
		_, _ = w.Write([]byte(`{"protocol":1,"agent_slug":"loom-curator","instance_id":"instance-one","status":"queued"}`))
	}))
	defer server.Close()
	call("plugin/init", subprocess.InitParams{Identity: fixtureIdentity(t, server.URL)})
	call("plugin/load", map[string]any{})
	var response subprocess.HTTPResponse
	raw := call("http/handle", subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/curator-wake", Body: []byte(`{"fragment":{"id":"wire-fragment"}}`)})
	if err = json.Unmarshal(raw, &response); err != nil || response.Status != 200 || !bytes.Contains(response.Body, []byte(`"status":"queued"`)) || requests.Load() != 1 {
		t.Fatalf("SDK callback: %#v %v", response, err)
	}
	call("plugin/unload", map[string]any{})
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatalf("SDK exit: %v %s", err, diagnostics.String())
	}
}
