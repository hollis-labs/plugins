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
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("MEDIA_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&oembedPlugin{}); err != nil {
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
		if r.URL.Query().Get("url") != "https://custom.example/post/42" {
			t.Error("provider URL changed")
		}
		_, _ = w.Write([]byte(`{"type":"video","title":"Wire preview","html":"<script>untrusted()</script>"}`))
	}))
	defer server.Close()
	config, err := json.Marshal([]extraProvider{{Name: "Fixture", Pattern: "^https://custom.example/post/", Endpoint: server.URL + "/?url={url}"}})
	if err != nil {
		t.Fatal(err)
	}
	call("plugin/init", subprocess.InitParams{Config: map[string]string{"oembed_extra_providers": string(config)}})
	call("plugin/load", map[string]any{})
	request := subprocess.EventHandleParams{Type: "message.sent", SessionID: "session-one", Data: map[string]any{"content": "https://custom.example/post/42"}}
	for i := 0; i < 2; i++ {
		var result subprocess.EventResult
		if err = json.Unmarshal(call("event/handle", request), &result); err != nil {
			t.Fatal(err)
		}
		if result.Cancel || len(result.Envelopes) != 1 || result.Envelopes[0].Type != envelopeType || result.Envelopes[0].Data["title"] != "Wire preview" {
			t.Fatalf("wire event: %#v", result)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("wire event bypassed cache")
	}
	request.PreHook = true
	var result subprocess.EventResult
	if err = json.Unmarshal(call("event/handle", request), &result); err != nil || len(result.Envelopes) != 0 {
		t.Fatal("pre-hook produced preview")
	}
	call("plugin/unload", map[string]any{})
	if err = input.Close(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatalf("SDK exit: %v %s", err, diagnostics.String())
	}
}
