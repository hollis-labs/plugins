package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func hostFixture(t *testing.T, dir string, committed bool) (subprocess.InitParams, *atomic.Bool) {
	t.Helper()
	receipt := fixtureExport(t, dir, "workspace-a", originalRows())
	var published atomic.Bool
	published.Store(committed)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+base64.RawURLEncoding.EncodeToString(make([]byte, 32)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resource := pluginapi.QueryResource(r.URL.Path[len("/api/plugin-host/query/"):])
		var data any
		switch resource {
		case pluginapi.QueryDataExports:
			rows := []pluginapi.DataExportReceipt{}
			if published.Load() {
				rows = append(rows, receipt)
			}
			data = pluginapi.QueryDataExportsData{Exports: rows}
		case pluginapi.QueryMessageReferences:
			session := r.URL.Query().Get("session_id")
			message := r.URL.Query().Get("message_id")
			if message == "" {
				message = "m-valid"
			}
			if session != "session-a" || message != "m-valid" {
				http.Error(w, "reference unavailable", 404)
				return
			}
			data = pluginapi.QueryMessageReferencesData{References: []pluginapi.QueryMessageReference{{SessionID: session, MessageID: message, Role: "assistant", CreatedAt: "now"}}}
		default:
			http.Error(w, "unapproved resource", http.StatusForbidden)
			return
		}
		raw, err := json.Marshal(data)
		if err != nil {
			t.Error(err)
			return
		}
		if err = json.NewEncoder(w).Encode(pluginapi.QueryResponse{Protocol: 1, Resource: resource, SessionID: r.URL.Query().Get("session_id"), Data: raw}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	grant := pluginapi.QueryGrant{Protocol: 1, PluginID: pluginID, HostURL: server.URL, Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Scope: pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QueryMessageReferences, pluginapi.QueryDataExports}, AllSessions: true}}
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
	if err = m.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err = pluginapi.DecodeBlock(m.Nanite); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	params, published := hostFixture(t, dir, false)
	p := &bookmarks{}
	init, err := p.Init(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	if init.ID != m.ID || init.Version != m.Version || m.Hosts["nanite"].Min != "0.1.6" {
		t.Fatal("runtime contract mismatch")
	}
	// A valid but uncommitted export file already exists. It cannot authorize writes.
	req := subprocess.MCPCallRequest{ToolName: "bookmarks_save", SessionID: "session-a", Arguments: map[string]any{"message_id": "m-valid", "session_id": "session-b"}}
	if _, err = p.MCPCallTool(context.Background(), req); err == nil {
		t.Fatal("uncommitted file authorized write")
	}
	published.Store(true)
	result, err := p.MCPCallTool(context.Background(), req)
	if err != nil || !json.Valid(result.Content) {
		t.Fatalf("save failed: %v", err)
	}
	rows, err := p.db.list(context.Background(), "workspace-a", "session-a")
	if err != nil || len(rows) != 2 {
		t.Fatalf("canonical session not used: %#v %v", rows, err)
	}
	if _, err = p.Command(context.Background(), subprocess.CommandRequest{Name: "bookmark", SessionID: "session-a"}); err != nil {
		t.Fatalf("latest reply command: %v", err)
	}
	req.SessionID = "session-b"
	if _, err = p.MCPCallTool(context.Background(), req); err == nil {
		t.Fatal("forged reference accepted")
	}
	if err = p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = p.MCPCallTool(context.Background(), req); err == nil {
		t.Fatal("unloaded plugin accepted request")
	}
}
func TestHTTPRoundtripAndReferenceValidation(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &bookmarks{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, query, body string) subprocess.HTTPResponse {
		t.Helper()
		response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: method, Path: "/api/plugins/" + pluginID + path, RawQuery: query, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response := call("GET", "/bookmarks", "session_id=session-a", "")
	if response.Status != 200 || !bytes.Contains(response.Body, []byte("gone-message")) {
		t.Fatalf("imported orphan hidden: %d %s", response.Status, response.Body)
	}
	response = call("POST", "/bookmarks", "", `{"session_id":"session-b","message_id":"m-valid"}`)
	if response.Status == 200 {
		t.Fatal("mismatched session accepted")
	}
	response = call("POST", "/bookmarks", "", `{"session_id":"session-a","message_id":"m-valid","note":"Saved"}`)
	if response.Status != 200 {
		t.Fatalf("create failed: %d %s", response.Status, response.Body)
	}
	var saved struct {
		Bookmark bookmark `json:"bookmark"`
	}
	if err := json.Unmarshal(response.Body, &saved); err != nil {
		t.Fatal(err)
	}
	response = call("PATCH", "/bookmarks/"+saved.Bookmark.ID, "", `{"note":"Edited"}`)
	if response.Status != 200 {
		t.Fatalf("edit failed: %s", response.Body)
	}
	response = call("POST", "/toggle", "", `{"session_id":"session-a","message_id":"m-valid"}`)
	if response.Status != 200 || !bytes.Contains(response.Body, []byte(`"removed":true`)) {
		t.Fatalf("toggle failed: %s", response.Body)
	}
	response = call("DELETE", "/bookmarks/"+saved.Bookmark.ID, "", "")
	if response.Status != 404 {
		t.Fatal("deleted bookmark resurrected")
	}
	response = call("POST", "/bookmarks", "", `{"session_id":"session-a","message_id":"m-valid","unknown":true}`)
	if response.Status != 400 {
		t.Fatal("accepted unknown field")
	}
}
func TestSDKHelperProcess(t *testing.T) {
	if os.Getenv("BOOKMARKS_TEST_CHILD") != "1" {
		return
	}
	if err := subprocess.Serve(&bookmarks{}); err != nil {
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
		child.Env = append(os.Environ(), "BOOKMARKS_TEST_CHILD=1", "GORACE=atexit_sleep_ms=0")
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
		if save {
			call("mcp/call_tool", subprocess.MCPCallRequest{ToolName: "bookmarks_save", SessionID: "session-a", Arguments: map[string]any{"message_id": "m-valid", "note": "wire title"}})
		}
		raw := call("http/handle", subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/bookmarks", RawQuery: "session_id=session-a"})
		var response subprocess.HTTPResponse
		if err = json.Unmarshal(raw, &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != 200 || !bytes.Contains(response.Body, []byte("wire title")) {
			t.Fatalf("wire data missing: %s", raw)
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
