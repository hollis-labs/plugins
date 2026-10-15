package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func typedDocument(d document) []pluginapi.DataCell {
	row := make([]pluginapi.DataCell, len(requiredColumns)+1)
	for i := range row {
		row[i] = pluginapi.DataCell{Kind: "null"}
	}
	index := map[string]int{}
	for i, name := range requiredColumns {
		index[name] = i
	}
	for name, value := range map[string]string{"id": d.ID, "session_id": d.SessionID, "name": d.Name, "mime_type": d.MimeType, "content": d.Content, "summary": d.Summary, "created_at": d.CreatedAt, "updated_at": d.UpdatedAt} {
		row[index[name]] = cell(value)
	}
	row[index["size_bytes"]] = pluginapi.DataCell{Kind: "integer", Text: fmt.Sprint(d.SizeBytes)}
	row[index["included"]] = boolCell(d.Included)
	row[index["full_content"]] = boolCell(d.FullContent)
	return row
}
func TestReplayAfterExportRemovalAndRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newDatabase(dir)
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	summary := "user edit"
	if err := db.change(ctx, receipt.SourceID, "session-a", "old-full", patchRequest{Summary: &summary}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.change(ctx, receipt.SourceID, "session-a", "old-pointer", patchRequest{}, true); err != nil {
		t.Fatal(err)
	}
	before := readTable(t, dir, receipt.SourceID)
	if err := os.Remove(filepath.Join(dir, receipt.Path)); err != nil {
		t.Fatal(err)
	}
	restarted := newDatabase(dir)
	if err := restarted.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID), before) {
		t.Fatal("replay changed committed state")
	}
	bad := receipt
	bad.SHA256 = strings.Repeat("0", 64)
	if err := restarted.importReceipt(ctx, bad); err == nil {
		t.Fatal("different checkpoint accepted")
	}
}
func TestMCPAuthorityIsReadAndCreateOnly(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	ctx := context.Background()
	if _, err := p.Init(ctx, params); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"name": "forged", "content": "x", "included": true},
		{"name": "forged", "content": "x", "full_content": false},
	} {
		if _, err := p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: "documents_create", SessionID: "session-a", Arguments: args}); err == nil {
			t.Fatal("agent set context authority")
		}
	}
	// These names are refused for imported rows, UI-created rows, and other
	// sessions alike, so there is no provenance ambiguity or migration exception.
	user, err := p.create(ctx, "session-a", createRequest{Name: "User", Content: "界", Included: true, FullContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if user.SizeBytes != 3 {
		t.Fatal("non-ASCII size counted runes")
	}
	for _, session := range []string{"session-a", "session-b"} {
		for _, id := range []string{"old-full", user.ID} {
			for _, tool := range []string{"documents_update", "documents_delete"} {
				if _, err := p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: tool, SessionID: session, Arguments: map[string]any{"id": id, "included": false}}); err == nil {
					t.Fatal("agent mutation accepted", tool)
				}
			}
			if session == "session-b" {
				if _, err := p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: "documents_get", SessionID: session, Arguments: map[string]any{"id": id}}); !errors.Is(err, errNotFound) {
					t.Fatal("cross-session MCP read", err)
				}
			}
		}
	}
	if _, err := p.MCPCallTool(ctx, subprocess.MCPCallRequest{ToolName: "documents_create", SessionID: "session-a", Arguments: map[string]any{"name": "Agent", "content": "作業"}}); err != nil {
		t.Fatal(err)
	}
	source, _ := p.ready(ctx)
	rows, err := p.db.list(ctx, source, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rows {
		if d.Name == "Agent" && (d.Included || d.FullContent || d.SizeBytes != 6) {
			t.Fatal("agent document settings", d)
		}
	}
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	block, err := pluginapi.DecodeBlock(m.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	if block.Registers.Slots[0].Slot != pluginapi.SlotPrimaryDrawer || block.Registers.Slots[0].Icon != "file-text" {
		t.Fatal("primary slot/icon", block)
	}
	for _, tool := range m.Tools {
		if tool.Name == "documents_update" || tool.Name == "documents_delete" {
			t.Fatal("forbidden tool declared")
		}
		if tool.Name == "documents_create" && (strings.Contains(string(tool.InputSchema), "included") || strings.Contains(string(tool.InputSchema), "full_content")) {
			t.Fatal("agent context settings declared")
		}
	}
}
func TestContextGoldenFallbackOrderAndBounds(t *testing.T) {
	// Authored from origin/main internal/chat/context_client.go's
	// buildUserContextSlot, not from this plugin's formatter.
	d := document{ID: "doc-id", Name: "設計", Content: "full\ntext", Summary: "summary", SizeBytes: 9, Included: true, FullContent: true}
	for _, test := range []struct {
		full    bool
		summary string
		want    string
	}{
		{true, "summary", "### Document: 設計\nfull\ntext"},
		{false, "summary", "### Document: 設計 (pointer)\nsummary"},
		{false, "", "### Document: 設計 (pointer)\n(document ID: doc-id, size: 9 bytes)"},
	} {
		d.Summary = test.summary
		if got := documentContext(d, test.full); got != test.want {
			t.Fatalf("core string mismatch: %q", got)
		}
	}
	d.Content = strings.Repeat("x", 10000)
	d.Summary = "small pointer"
	response := buildDocumentContext([]document{d}, 500)
	if len(response.Items) != 1 || response.Items[0].Content != "### Document: 設計 (pointer)\nsmall pointer" {
		t.Fatal("full text did not fall back", response)
	}
	if got := buildDocumentContext([]document{d}, 1); len(got.Items) != 0 {
		t.Fatal("pointer exceeded budget")
	}
	rows := make([]document, 140)
	for i := range rows {
		rows[i] = document{ID: fmt.Sprintf("d-%03d", i), Name: "small", Included: true}
	}
	response = buildDocumentContext(rows, 50000)
	if len(response.Items) != pluginapi.MaxContextItems {
		t.Fatal("item cap", len(response.Items))
	}
	for i := 1; i < len(response.Items); i++ {
		if response.Items[i-1].Relevance <= response.Items[i].Relevance {
			t.Fatal("unstable broker tie")
		}
	}
	// Exercise the serialized-byte truncation path independently of the host's
	// tighter token budget, and verify entire items remain valid.
	for i := range rows {
		rows[i].FullContent = true
		rows[i].Content = strings.Repeat("\x01", 4096)
	}
	response = buildDocumentContext(rows, 2<<20)
	raw, _ := json.Marshal(response)
	if len(raw) > pluginapi.MaxContextBytes || len(response.Items) >= len(rows) {
		t.Fatal("serialized cap")
	}
	if _, err := pluginapi.DecodeContextResponse(raw); err != nil {
		t.Fatal(err)
	}
	a := document{ID: "z", CreatedAt: "2026-10-02T12:00:00Z"}
	b := document{ID: "a", CreatedAt: "2026-10-02T12:00:00.5Z"}
	if documentOrder(a, b) >= 0 {
		t.Fatal("RFC3339Nano lexical ordering")
	}
	b.CreatedAt = a.CreatedAt
	if documentOrder(b, a) >= 0 {
		t.Fatal("equal timestamp tie is not deterministic")
	}
}
func TestLegacyNonzeroAndNativeByteSize(t *testing.T) {
	rows := legacyRows()
	rows[0][6] = pluginapi.DataCell{Kind: "integer", Text: "2"}
	rows[0][7] = pluginapi.DataCell{Kind: "integer", Text: "-2"}
	dir := t.TempDir()
	receipt := fixtureExport(t, dir, "workspace-a", rows)
	db := newDatabase(dir)
	ctx := context.Background()
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	docs, err := db.list(ctx, receipt.SourceID, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range docs {
		if d.ID == "old-full" {
			found = true
			if !d.Included || !d.FullContent {
				t.Fatal("nonzero became false")
			}
		}
	}
	if !found {
		t.Fatal("legacy row missing")
	}
}
func TestCacheInvalidatesAcrossWritersAndFailedMutations(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newDatabase(dir)
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	before, err := db.list(ctx, receipt.SourceID, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	// Failed callbacks must not mutate the cached committed slices.
	failed := errors.New("abort")
	if err := db.transaction(ctx, receipt.SourceID, func(s *table) (bool, error) { s.Snapshot.Rows[0][8] = cell("poisoned cache"); return false, failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	after, err := db.list(ctx, receipt.SourceID, "session-a")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed mutation poisoned cache", err)
	}
	path := filepath.Join(dir, sourceFile(receipt.SourceID))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	independent := newDatabase(dir)
	if err = independent.transaction(ctx, receipt.SourceID, func(s *table) (bool, error) { s.Snapshot.Rows[1][8] = cell("new--summary"); return true, nil }); err != nil {
		t.Fatal(err)
	}
	updated, err := os.Stat(path)
	if err != nil || updated.Size() != info.Size() {
		t.Fatal("fixture must retain byte size", err)
	}
	if err = os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// Inode alone distinguishes this atomic replacement.
	after, err = db.list(ctx, receipt.SourceID, "session-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range after {
		if d.ID == "old-pointer" && d.Summary != "new--summary" {
			t.Fatal("cached old inode")
		}
	}
}
func TestSessionQuotasAndGrowthPolicy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rows := [][]pluginapi.DataCell{}
	for i := range maxSessionDocuments {
		rows = append(rows, typedDocument(document{ID: fmt.Sprint(i), SessionID: "session-a", Name: "old"}))
	}
	db := newDatabase(dir)
	receipt := fixtureExport(t, dir, "workspace-a", rows)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-a", Name: "over"}); !errors.Is(err, errQuota) {
		t.Fatal("count quota", err)
	}
	if _, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-b", Name: "other"}); err != nil {
		t.Fatal(err)
	}
	if err := checkSessionQuota(1, maxSessionBytes+1); !errors.Is(err, errQuota) {
		t.Fatal("byte quota")
	}
	if err := checkSessionQuota(maxSessionDocuments, maxSessionBytes); err != nil {
		t.Fatal("quota boundary", err)
	}
	// Imported sessions over native quotas remain lossless and manageable.
	dir = t.TempDir()
	rows = [][]pluginapi.DataCell{}
	for i := range 17 {
		rows = append(rows, typedDocument(document{ID: fmt.Sprint(i), SessionID: "session-a", Name: "old", Content: strings.Repeat("x", 512<<10)}))
	}
	db = newDatabase(dir)
	receipt = fixtureExport(t, dir, "workspace-a", rows)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-a", Name: "tiny", Content: "x"}); !errors.Is(err, errQuota) {
		t.Fatal("byte growth accepted", err)
	}
	if err := db.change(ctx, receipt.SourceID, "session-a", "0", patchRequest{}, true); err != nil {
		t.Fatal("legacy deletion blocked", err)
	}
}
func TestMetadataResponseTruncation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rows := [][]pluginapi.DataCell{}
	for i := range 5 {
		rows = append(rows, typedDocument(document{ID: fmt.Sprint(i), SessionID: "session-a", Name: strings.Repeat("\x01", 100000)}))
	}
	db := newDatabase(dir)
	receipt := fixtureExport(t, dir, "workspace-a", rows)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	page, err := db.page(ctx, receipt.SourceID, "session-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	if len(raw) > maxResponseBytes || !page.More || len(page.Documents) != 3 || page.NextOffset != 3 {
		t.Fatal("response not truncated by whole row", len(raw), page.NextOffset)
	}
	next, err := db.page(ctx, receipt.SourceID, "session-a", page.NextOffset)
	if err != nil || next.More || len(next.Documents) != 2 {
		t.Fatal("truncated page lost rows", err)
	}
}
func TestCrashLeftoversAndErrorAfterRename(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newDatabase(dir)
	receipt := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, sourceFile(receipt.SourceID)+"."+newID()+".tmp")
	unrelated := filepath.Join(dir, "unrelated.tmp")
	for _, path := range []string{leftover, unrelated} {
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db = newDatabase(dir)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("crash temporary retained")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated temporary removed")
	}
	db.syncDirectory = func(*os.File) error { return errors.New("injected directory sync failure") }
	created, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-a", Name: "visible"})
	if !errors.Is(err, errCommitUncertain) {
		t.Fatal("post-rename outcome hidden", err)
	}
	rows, err := newDatabase(dir).list(ctx, receipt.SourceID, "session-a")
	if err != nil || len(rows) != 1 || rows[0].ID != created.ID {
		t.Fatal("renamed commit missing or retried", err)
	}
}
func queryFixture(t *testing.T, params subprocess.InitParams, handler http.Handler) subprocess.InitParams {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		t.Fatal(err)
	}
	grant.HostURL = server.URL
	params.Identity, err = json.Marshal(map[string]any{"nanite_host_query": grant})
	if err != nil {
		t.Fatal(err)
	}
	return params
}
func TestReadyAmbiguousAndMoreReceipts(t *testing.T) {
	for _, more := range []bool{false, true} {
		t.Run(fmt.Sprint(more), func(t *testing.T) {
			params, _ := hostFixture(t, t.TempDir(), true)
			params = queryFixture(t, params, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, _ := json.Marshal(pluginapi.QueryDataExportsData{More: more, Exports: []pluginapi.DataExportReceipt{{PluginID: pluginID, Feature: "documents", SourceID: "a"}, {PluginID: pluginID, Feature: "documents", SourceID: "b"}}})
				_ = json.NewEncoder(w).Encode(pluginapi.QueryResponse{Protocol: 1, Resource: pluginapi.QueryDataExports, Data: data})
			}))
			p := &documentsPlugin{}
			if _, err := p.Init(context.Background(), params); err != nil {
				t.Fatal(err)
			}
			if _, err := p.ready(context.Background()); err == nil {
				t.Fatal("incomplete or ambiguous receipt accepted")
			}
		})
	}
}
func TestReadyStallDoesNotHoldStateMutex(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	entered := make(chan struct{})
	var once sync.Once
	params = queryFixture(t, params, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { once.Do(func() { close(entered) }); <-r.Context().Done() }))
	p := &documentsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	first, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.ready(first); done <- err }()
	<-entered
	second, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	next := make(chan error, 1)
	go func() { _, err := p.ready(second); next <- err }()
	select {
	case err := <-next:
		if err == nil {
			t.Fatal("stalled call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled caller blocked on state mutex")
	}
	if err := p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}
func TestStorageFailuresAreServerErrors(t *testing.T) {
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &documentsPlugin{}
	ctx := context.Background()
	if _, err := p.Init(ctx, params); err != nil {
		t.Fatal(err)
	}
	source, err := p.ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(params.DataDir, sourceFile(source)), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/documents", RawQuery: "session_id=session-a"})
	if err != nil || got.Status != 500 {
		t.Fatal("storage error blamed caller", got.Status, err)
	}
}
