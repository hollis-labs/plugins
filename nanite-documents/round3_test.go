package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestLegacyNonGrowingChangesAboveNativeCap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rows := make([][]pluginapi.DataCell, 3)
	for i := range rows {
		rows[i] = typedDocument(document{ID: fmt.Sprint(i), SessionID: "session-a", Name: "legacy", Content: strings.Repeat("x", 500<<10), Summary: "seed", CreatedAt: "2026-10-02T00:00:00Z", UpdatedAt: "2026-10-02T00:00:00Z"})
	}
	db := newDatabase(dir)
	db.nativeGrowthCap = 1 << 20
	receipt := fixtureExport(t, dir, "workspace-a", rows)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{Included: &on, FullContent: &on}, false); err != nil {
		t.Fatal("toggle/timestamp growth refused", err)
	}
	got, err := db.read(ctx, receipt.SourceID, "session-a", "0", 0, 1)
	if err != nil || !got.Document.Included || !got.Document.FullContent {
		t.Fatal("settings missing", err)
	}
	same := "seed"
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{Summary: &same}, false); err != nil {
		t.Fatal("unchanged summary refused", err)
	}
	growth := "seed grows"
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{Summary: &growth}, false); !errors.Is(err, errQuota) {
		t.Fatal("summary growth bypassed cap", err)
	}
	if _, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-b", Name: "new"}); !errors.Is(err, errQuota) {
		t.Fatal("new row bypassed total cap", err)
	}
	shorter := ""
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{Summary: &shorter}, false); err != nil {
		t.Fatal("shrinking summary refused", err)
	}
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{}, true); err != nil {
		t.Fatal("delete refused", err)
	}
}

func decodeMCP(t *testing.T, result subprocess.MCPCallResult, target any) {
	t.Helper()
	var content []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result.Content, &content); err != nil {
		t.Fatal(err)
	}
	if len(content) != 1 {
		t.Fatal("missing tool result")
	}
	if err := json.Unmarshal([]byte(content[0].Text), target); err != nil {
		t.Fatal(err)
	}
}
func TestMCPReadsHonorUserInclusion(t *testing.T) {
	ctx := context.Background()
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	if _, err := p.Init(ctx, params); err != nil {
		t.Fatal(err)
	}
	excludedPointer, err := p.create(ctx, "session-a", createRequest{Name: "excluded pointer", Content: "secret", FullContent: false})
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.create(ctx, "session-b", createRequest{Name: "other session", Content: "other", Included: true, FullContent: false})
	if err != nil {
		t.Fatal(err)
	}
	call := func(tool, session string, args map[string]any) (subprocess.MCPCallResult, error) {
		return p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: tool, SessionID: session, Arguments: args})
	}
	listed, err := call("documents_list", "session-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	var page documentPage
	decodeMCP(t, listed, &page)
	if len(page.Documents) != 2 || page.More {
		t.Fatal("excluded rows listed", page)
	}
	seen := map[string]bool{}
	for _, d := range page.Documents {
		seen[d.ID] = true
		if !d.Included || d.Content != "" {
			t.Fatal("list visibility/content", d)
		}
	}
	if !seen["old-full"] || !seen["old-pointer"] {
		t.Fatal("included full/pointer missing", seen)
	}
	listed, err = call("documents_list", "session-a", map[string]any{"offset": 1})
	if err != nil {
		t.Fatal(err)
	}
	decodeMCP(t, listed, &page)
	if len(page.Documents) != 1 || page.NextOffset != 2 {
		t.Fatal("pagination counted excluded rows", page)
	}
	for _, id := range []string{"old-full", "old-pointer"} {
		result, err := call("documents_get", "session-a", map[string]any{"id": id})
		if err != nil {
			t.Fatal("included full/pointer unreadable", err)
		}
		var read documentRead
		decodeMCP(t, result, &read)
		if read.Document.Content == "" {
			t.Fatal("included content missing")
		}
		if _, err := call("documents_get", "session-b", map[string]any{"id": id}); !errors.Is(err, errNotFound) {
			t.Fatal("cross-session read", err)
		}
	}
	for _, id := range []string{"excluded", excludedPointer.ID, other.ID} {
		if _, err := call("documents_get", "session-a", map[string]any{"id": id}); !errors.Is(err, errNotFound) {
			t.Fatal("excluded/cross-session visibility differs", err)
		}
	}
	created, err := call("documents_create", "session-a", map[string]any{"name": "Agent", "content": "agent content"})
	if err != nil {
		t.Fatal(err)
	}
	var agent document
	decodeMCP(t, created, &agent)
	if _, err := call("documents_get", "session-a", map[string]any{"id": agent.ID}); !errors.Is(err, errNotFound) {
		t.Fatal("agent-created exclusion ignored", err)
	}
	source, err := p.ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if err := p.db.change(ctx, source, "session-a", agent.ID, patchRequest{Included: &on}, false); err != nil {
		t.Fatal(err)
	}
	result, err := call("documents_get", "session-a", map[string]any{"id": agent.ID})
	if err != nil {
		t.Fatal("user inclusion did not grant read", err)
	}
	var read documentRead
	decodeMCP(t, result, &read)
	if read.Document.Content != "agent content" {
		t.Fatal("agent content missing")
	}
	response, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/documents/excluded", RawQuery: "session_id=session-a"})
	if err != nil || response.Status != http.StatusOK {
		t.Fatal("HTTP exclusion behavior changed", response.Status, err)
	}
}

func TestHTTPQuotaMappingAndNonGrowingChanges(t *testing.T) {
	for _, kind := range []string{"total", "session-count"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			params, _ := hostFixture(t, t.TempDir(), true)
			p := &documentsPlugin{}
			if _, err := p.Init(ctx, params); err != nil {
				t.Fatal(err)
			}
			source, err := p.ready(ctx)
			if err != nil {
				t.Fatal(err)
			}
			target := "old-full"
			if kind == "total" {
				p.db.nativeGrowthCap = 1
			} else {
				target = "0"
				if err := p.db.transaction(ctx, source, func(state *table) (bool, error) {
					state.Snapshot.Rows = make([][]pluginapi.DataCell, maxSessionDocuments)
					for i := range state.Snapshot.Rows {
						state.Snapshot.Rows[i] = typedDocument(document{ID: fmt.Sprint(i), SessionID: "session-a", Name: "legacy"})
					}
					return true, nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			call := func(method, path, body string) subprocess.HTTPResponse {
				t.Helper()
				response, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: method, Path: "/api/plugins/" + pluginID + path, RawQuery: "session_id=session-a", Body: []byte(body)})
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			response := call("POST", "/documents", `{"name":"new","content":"x"}`)
			if response.Status != http.StatusRequestEntityTooLarge || !strings.Contains(string(response.Body), "document quota exceeded") {
				t.Fatal("quota not actionable 413", response)
			}
			if got := call("PATCH", "/documents/"+target, `{"included":true,"full_content":true}`); got.Status != http.StatusOK {
				t.Fatal("non-growing patch blocked", got)
			}
			if kind == "total" {
				if got := call("PATCH", "/documents/"+target, `{"summary":"a longer summary that grows content"}`); got.Status != http.StatusRequestEntityTooLarge {
					t.Fatal("summary cap bypassed", got)
				}
			}
			if got := call("DELETE", "/documents/"+target, ""); got.Status != http.StatusOK {
				t.Fatal("quota blocked deletion", got)
			}
			if kind == "session-count" {
				if got := call("POST", "/documents", `{"name":"new","content":"x"}`); got.Status != http.StatusOK {
					t.Fatal("freed quota unavailable", got)
				}
			}
		})
	}
}
func TestHTTPHostSessionFailureIsServerError(t *testing.T) {
	ctx := context.Background()
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	if _, err := p.Init(ctx, params); err != nil {
		t.Fatal(err)
	}
	source, err := p.ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	params = queryFixture(t, params, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "host failed", http.StatusInternalServerError)
	}))
	if _, err := p.Init(ctx, params); err != nil {
		t.Fatal(err)
	}
	p.source = source
	response, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a"})
	if err != nil || response.Status != http.StatusServiceUnavailable {
		t.Fatal("host failure blamed caller", response.Status, err)
	}
}
