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
	block, err := pluginapi.DecodeBlock(m.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pluginapi.ContextScopeFor(block, m.Capabilities); err != nil {
		t.Fatal(err)
	}
	params, published := hostFixture(t, t.TempDir(), false)
	p := &documentsPlugin{}
	if _, err = p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	call := subprocess.MCPCallRequest{ToolName: "documents_create", SessionID: "session-a", Arguments: map[string]any{"name": "Doc", "content": "new content"}}
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("orphan export authorized writes")
	}
	published.Store(true)
	if _, err = p.MCPCallTool(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	call.Arguments["session_id"] = "session-b"
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("spoof accepted")
	}
	if err = p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.MCPCallTool(context.Background(), call); err == nil {
		t.Fatal("unloaded access")
	}
}
func fetchContext(t *testing.T, p *documentsPlugin, budget int) pluginapi.ContextResponse {
	t.Helper()
	body, _ := json.Marshal(pluginapi.ContextRequest{Protocol: 1, SourceID: "documents", SessionID: "session-a", TokenBudget: budget})
	response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: pluginapi.ContextFetchPath, SessionID: "session-a", Body: body})
	if err != nil || response.Status != 200 {
		t.Fatal(response, err)
	}
	decoded, err := pluginapi.DecodeContextResponse(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
func TestContextModesBudgetAndNoConsumption(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if got := fetchContext(t, p, 1); len(got.Items) != 0 {
		t.Fatal("budget bypass")
	}
	for range 2 {
		got := fetchContext(t, p, 5000)
		raw, _ := json.Marshal(got)
		if len(got.Items) != 2 || !bytes.Contains(raw, []byte("retained full content")) || !bytes.Contains(raw, []byte("only summary")) || bytes.Contains(raw, []byte("hidden pointer content")) || bytes.Contains(raw, []byte("excluded content")) {
			t.Fatal("context modes", string(raw))
		}
	}
	included := false
	source, _ := p.ready(context.Background())
	if err := p.db.change(context.Background(), source, "session-a", "old-full", patchRequest{Included: &included}, false); err != nil {
		t.Fatal(err)
	}
	if got := fetchContext(t, p, 5000); len(got.Items) != 1 || got.Items[0].Key != "old-pointer" {
		t.Fatal("excluded document injected", got)
	}
}
func TestHTTPScopesStrictBodiesAndLimits(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
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
	for _, body := range []string{`{"name":"Doc","content":"a","session_id":"session-b"}`, `{"name":"Doc","content":"a","content":"b"}`, `{"name":"Doc","content":"a"} {}`, `{"content":"a"}`, `{"name":"Doc"}`, `{"name":"Doc","content":null}`, `{"name":"Doc","content":"a","Content":"b"}`, `{"name":"Doc","content":"a","included":null}`, `{"name":"Doc","content":"\u0000"}`} {
		if got := call("POST", "/documents", "session-a", body); got.Status != 400 {
			t.Fatal("invalid body accepted", body, got)
		}
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		body := ""
		if method == "PATCH" {
			body = `{"included":true}`
		}
		if got := call(method, "/documents/old-full", "session-b", body); got.Status != 404 {
			t.Fatal("cross-session access", method, got)
		}
	}
	if got := call("PATCH", "/documents/old-full", "session-a", `{}`); got.Status != 400 {
		t.Fatal("empty patch accepted")
	}
	if got := call("PATCH", "/documents/old-full", "session-a", `{"summary":"new summary"}`); got.Status != 200 {
		t.Fatal(got)
	}
	if got := call("GET", "/documents", "session-a", ""); got.Status != 200 || bytes.Contains(got.Body, []byte("retained full content")) {
		t.Fatal("metadata list leaked content", got)
	}
	response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-b", SessionID: "session-a"})
	if err != nil || response.Status != 403 {
		t.Fatal("SDK identity mismatch", response, err)
	}
	raw, _ := json.Marshal(createRequest{Name: "large", Content: strings.Repeat("x", maxContentBytes+1)})
	if got := call("POST", "/documents", "session-a", string(raw)); got.Status != 400 {
		t.Fatal("content limit bypass")
	}

	oversized, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a", Body: []byte(strings.Repeat(" ", pluginapi.MaxHTTPBody+1))})
	if err == nil {
		t.Fatal("SDK oversized input accepted", oversized.Status)
	}

}
func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("DOCUMENTS_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&documentsPlugin{}); err != nil {
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
		child.Env = append(os.Environ(), "DOCUMENTS_TEST_CHILD=1", "GORACE=atexit_sleep_ms=0")
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
		listRaw := call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "documents_list", SessionID: "session-a"})
		var listResult subprocess.MCPCallResult
		if err = json.Unmarshal(listRaw, &listResult); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(listResult.Content) || !bytes.Contains(listResult.Content, []byte("old-full")) {
			t.Fatal("SDK list content missing", string(listRaw))
		}
		if save {
			call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "documents_create", SessionID: "session-a", Arguments: map[string]any{"name": "Wire", "content": "wire document", "included": true, "full_content": true}})
		}
		raw := call("http/handle", subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a"})
		var response subprocess.HTTPResponse
		if err = json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != 200 || !bytes.Contains(response.Body, []byte("Wire")) {
			t.Fatalf("wire data missing: %s", raw)
		}
		body, _ := json.Marshal(pluginapi.ContextRequest{Protocol: 1, SourceID: "documents", SessionID: "session-a", TokenBudget: 5000})
		contextRaw := call("http/handle", subprocess.HTTPRequest{Method: "POST", Path: pluginapi.ContextFetchPath, SessionID: "session-a", Body: body})
		var contextResponse subprocess.HTTPResponse
		if err = json.Unmarshal(contextRaw, &contextResponse); err != nil {
			t.Fatal(err)
		}
		decoded, decodeErr := pluginapi.DecodeContextResponse(contextResponse.Body)
		if contextResponse.Status != 200 || decodeErr != nil || len(decoded.Items) != 3 {
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

func TestMaximumContentAndInvalidRawUTF8(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("\x01", maxContentBytes)
	raw, _ := json.Marshal(createRequest{Name: "Maximum", Content: content})
	got, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a", Body: raw})
	if err != nil || got.Status != 200 {
		t.Fatal("maximum content rejected", got.Status, err)
	}
	var created document
	if err = json.Unmarshal(got.Body, &created); err != nil {
		t.Fatal(err)
	}
	source, _ := p.ready(context.Background())
	rows, err := p.db.list(context.Background(), source, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == created.ID {
			found = true
			if row.Content != content || row.SizeBytes != int64(len(content)) {
				t.Fatal("content/size changed")
			}
		}
	}
	if !found {
		t.Fatal("created document missing")
	}
	if _, err = p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "documents_create", SessionID: "session-a", Arguments: map[string]any{"name": "MCP maximum", "content": content}}); err != nil {
		t.Fatal(err)
	}
	raw = append([]byte(`{"name":"Doc","content":"`), 0xff)
	raw = append(raw, []byte(`"}`)...)
	got, err = p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a", Body: raw})
	if err != nil || got.Status != 400 {
		t.Fatal("invalid UTF-8 accepted", got.Status, err)
	}
}
