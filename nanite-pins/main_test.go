package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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

func hostFixture(t *testing.T, dir string, committed bool) (subprocess.InitParams, *atomic.Bool) {
	t.Helper()
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	var published atomic.Bool
	published.Store(committed)
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resource := pluginapi.QueryResource(strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/"))
		session := r.URL.Query().Get("session_id")
		var data any
		switch resource {
		case pluginapi.QueryDataExports:
			rows := []pluginapi.DataExportReceipt{}
			if published.Load() {
				rows = append(rows, receipt)
			}
			data = pluginapi.QueryDataExportsData{Exports: rows}
		case pluginapi.QuerySessions:
			if session != "session-a" && session != "session-b" {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			data = pluginapi.QuerySessionsData{Sessions: []pluginapi.QuerySession{{ID: session, ProjectID: "project-a"}}}
		default:
			http.Error(w, "unapproved", http.StatusForbidden)
			return
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Error(err)
			return
		}
		if err = json.NewEncoder(w).Encode(pluginapi.QueryResponse{Protocol: 1, Resource: resource, SessionID: session, Data: raw}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	grant := pluginapi.QueryGrant{Protocol: 1, PluginID: pluginID, HostURL: server.URL, Token: token, Scope: pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryDataExports}, AllSessions: true}}
	identity, err := json.Marshal(map[string]any{"nanite_host_query": grant})
	if err != nil {
		t.Fatal(err)
	}
	return subprocess.InitParams{DataDir: dir, Identity: identity}, &published
}
func TestDeclarationAndPreCutoverBoundary(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pluginapi.DecodeBlock(m.Nanite); err != nil {
		t.Fatal(err)
	}
	params, published := hostFixture(t, t.TempDir(), false)
	p := &pinsPlugin{}
	init, err := p.Init(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if init.ID != m.ID || init.Version != m.Version {
		t.Fatal("runtime manifest mismatch")
	}
	call := subprocess.MCPCallRequest{ToolName: "pins_set", SessionID: "session-a", Arguments: map[string]any{"content": "a pin"}}
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("orphan export authorized write")
	}
	published.Store(true)
	if _, err = p.MCPCallTool(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	call.Arguments["session_id"] = "session-b"
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("caller-supplied identity accepted")
	}
	if err = p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("unloaded write accepted")
	}
}
func TestContextBudgetDoesNotConsumePin(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &pinsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fetch := func(budget int) pluginapi.AlwaysShipResponse {
		t.Helper()
		body, err := json.Marshal(pluginapi.AlwaysShipRequest{Protocol: 1, SourceID: "pins", SessionID: "session-a", MaxBytes: budget})
		if err != nil {
			t.Fatal(err)
		}
		response, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: "POST", Path: pluginapi.AlwaysShipFetchPath, SessionID: "session-a", Body: body})
		if err != nil || response.Status != 200 {
			t.Fatal("fetch", response, err)
		}
		decoded, err := pluginapi.DecodeAlwaysShipResponse(response.Body, budget)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	if got := fetch(5000); !strings.Contains(got.Body, "[pinned] retained pin") || !strings.Contains(got.Body, "[pinned:project] project pin") {
		t.Fatal("due pins missing", got)
	}
	if got := fetch(5000); !strings.Contains(got.Body, "[pinned] retained pin") || !strings.Contains(got.Body, "[pinned:project] project pin") {
		t.Fatal("read consumed pins")
	}
	if _, err := p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: "pins_delete", SessionID: "session-a", Arguments: map[string]any{"id": "old-session"}}); err != nil {
		t.Fatal(err)
	}
	if got := fetch(5000); got.Body != "[pinned:project] project pin" {
		t.Fatal("deleted pin reappeared", got)
	}
}
func TestHTTPScopesAndStrictBodies(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &pinsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, session, body string) subprocess.HTTPResponse {
		t.Helper()
		response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: method, Path: "/api/plugins/" + pluginID + path, RawQuery: "session_id=" + session, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	for _, body := range []string{`{"content":"pin","session_id":"session-b"}`, `{"content":"pin","scope":"turn"}`, `{"content":"pin","content":"duplicate"}`, `{"content":"pin"} {}`} {
		if got := call("POST", "/pins", "session-a", body); got.Status != 400 {
			t.Fatal("invalid body accepted", got)
		}
	}
	if got := call("DELETE", "/pins/old-session", "session-b", ""); got.Status != 404 {
		t.Fatal("cross-session deletion", got)
	}
	if got := call("PATCH", "/pins/old-session/scope", "session-a", `{"scope":"project","project_id":"forged"}`); got.Status != 400 {
		t.Fatal("project spoof accepted", got)
	}
	if got := call("PATCH", "/pins/old-session/scope", "session-a", `{"scope":"project"}`); got.Status != 200 {
		t.Fatal("promotion failed", got)
	}
	if got := call("PATCH", "/pins/old-session", "session-b", `{"content":"edited project pin"}`); got.Status != 200 {
		t.Fatal("project edit failed", got)
	}
	if got := call("PATCH", "/pins/old-project/scope", "session-a", `{"scope":"session"}`); got.Status != 400 {
		t.Fatal("invented null origin", got)
	}
	if got := call("DELETE", "/pins/old-session", "session-b", ""); got.Status != 200 {
		t.Fatal("project deletion failed", got)
	}
	if got := call("GET", "/pins", "session-a", ""); got.Status != 200 || bytes.Contains(got.Body, []byte("old-session")) {
		t.Fatal("deleted row visible", got)
	}
	response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/pins", RawQuery: "session_id=session-b", SessionID: "session-a"})
	if err != nil || response.Status != 403 {
		t.Fatal("SDK identity mismatch accepted", response, err)
	}
}
func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("PINS_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&pinsPlugin{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}
func TestActualSDKProcessPersistsAcrossReconnect(t *testing.T) {
	dir := t.TempDir()
	params, _ := hostFixture(t, dir, true)
	run := func(save bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKHelperProcess$")
		child.Env = append(os.Environ(), "PINS_TEST_CHILD=1", "GORACE=atexit_sleep_ms=0")
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
		call("plugin/init", params)
		call("plugin/load", map[string]any{})
		listRaw := call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "pins_list", SessionID: "session-a"})
		var listResult subprocess.MCPCallResult
		if err = json.Unmarshal(listRaw, &listResult); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(listResult.Content) || !bytes.Contains(listResult.Content, []byte("old-session")) {
			t.Fatal("SDK list content missing", string(listRaw))
		}
		getRaw := call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "pins_get", SessionID: "session-a", Arguments: map[string]any{"id": "old-session"}})
		var getResult subprocess.MCPCallResult
		if err = json.Unmarshal(getRaw, &getResult); err != nil || !bytes.Contains(getResult.Content, []byte("retained pin")) || !bytes.Contains(getResult.Content, []byte("total_bytes")) {
			t.Fatal("SDK get content missing", string(getRaw), err)
		}
		if save {
			call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "pins_set", SessionID: "session-a", Arguments: map[string]any{"content": "wire pin"}})
		}
		raw := call("http/handle", subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/pins", RawQuery: "session_id=session-a"})
		var response subprocess.HTTPResponse
		if err = json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != 200 || !bytes.Contains(response.Body, []byte("wire pin")) {
			t.Fatalf("wire data missing: %s", raw)
		}
		body, _ := json.Marshal(pluginapi.AlwaysShipRequest{Protocol: 1, SourceID: "pins", SessionID: "session-a", MaxBytes: 5000})
		contextRaw := call("http/handle", subprocess.HTTPRequest{Method: "POST", Path: pluginapi.AlwaysShipFetchPath, SessionID: "session-a", Body: body})
		var contextResponse subprocess.HTTPResponse
		if err = json.Unmarshal(contextRaw, &contextResponse); err != nil {
			t.Fatal(err)
		}
		decoded, decodeErr := pluginapi.DecodeAlwaysShipResponse(contextResponse.Body, 5000)
		if contextResponse.Status != 200 || decodeErr != nil || !strings.Contains(decoded.Body, "[pinned] wire pin") || !strings.Contains(decoded.Body, "[pinned] retained pin") || !strings.Contains(decoded.Body, "[pinned:project] project pin") {
			t.Fatal("SDK context failed", contextResponse, decodeErr)
		}
		if err = input.Close(); err != nil {
			t.Fatal(err)
		}
		if err = child.Wait(); err != nil {
			t.Fatalf("SDK exit: %v %s", err, diagnostics.String())
		}
	}
	run(true)
	run(false)
}
