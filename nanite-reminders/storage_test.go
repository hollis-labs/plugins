package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

func fixtureExport(t *testing.T, dir, source string, rows [][]pluginapi.DataCell) pluginapi.DataExportReceipt {
	t.Helper()
	snapshot := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: "reminders", SourceID: source, Columns: append(append([]string{}, requiredColumns...), "future_metadata"), Rows: rows}
	raw, err := pluginapi.EncodeDataExport(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := "core-imports/" + source + ".jsonl"
	if err = os.MkdirAll(filepath.Join(dir, "core-imports"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, path), raw, 0600); err != nil {
		t.Fatal(err)
	}
	exported, err := pluginapi.DecodeDataExport(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pluginapi.DataExportReceipt{PluginID: pluginID, Feature: "reminders", SourceID: source, Path: path, SHA256: exported.SHA256, RowCount: len(rows)}
}
func cell(text string) pluginapi.DataCell { return pluginapi.DataCell{Kind: "text", Text: text} }
func legacyRows() [][]pluginapi.DataCell {
	return [][]pluginapi.DataCell{
		{cell("old-session"), cell("session-a"), cell("session"), {Kind: "null"}, cell("retained reminder"), cell(`{"type":"turn_count","n":2}`), {Kind: "null"}, cell("original timestamp"), cell("original updated"), {Kind: "integer", Text: "9223372036854775807"}},
		{cell("old-project"), cell("orphan-session"), cell("project"), cell("project-a"), cell("project reminder"), cell(`{"type":"time","at":"2025-01-01T00:00:00Z"}`), {Kind: "null"}, cell("2025-01-01"), cell("original updated"), {Kind: "blob", Text: "AP8="}},
		{cell("already-fired"), cell("session-a"), cell("session"), {Kind: "null"}, {Kind: "text_bytes", Text: base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe})}, cell(`{"type":"time","at":"2025-01-01T00:00:00Z"}`), cell("fired exactly"), cell("2025-01-01"), cell("original updated"), {Kind: "real", Text: "0.12345678901234568"}},
	}
}
func readTable(t *testing.T, dir, source string) table {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, sourceFile(source)))
	if err != nil {
		t.Fatal(err)
	}
	var result table
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestImportPreservesTypedRowsAndAcknowledgment(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	original := legacyRows()
	receipt := fixtureExport(t, dir, "workspace-a", original)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID).Snapshot.Rows, original) {
		t.Fatal("import changed typed cells")
	}
	rows, err := db.due(ctx, receipt.SourceID, "session-a", "project-a", 2, time.Now())
	if err != nil || len(rows) != 2 {
		t.Fatal("due legacy rows", rows, err)
	}
	// Retrieval neither acknowledges nor rewrites legacy cells.
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID).Snapshot.Rows, original) {
		t.Fatal("context retrieval consumed reminder")
	}
	if err = db.change(ctx, receipt.SourceID, "session-a", "project-a", "old-session", "ack", ""); err != nil {
		t.Fatal(err)
	}
	if err = db.change(ctx, receipt.SourceID, "session-a", "project-a", "old-project", "delete", ""); err != nil {
		t.Fatal(err)
	}
	after := readTable(t, dir, receipt.SourceID)
	if len(after.Snapshot.Rows) != 2 || after.Snapshot.Rows[0][6].Kind != "text" || after.Snapshot.Rows[0][9] != original[0][9] || !reflect.DeepEqual(after.Snapshot.Rows[1], original[2]) {
		t.Fatal("mutation changed unrelated cells")
	}
	if err = db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID), after) {
		t.Fatal("replay resurrected acknowledged/deleted rows")
	}
	conflict := fixtureExport(t, dir, receipt.SourceID, [][]pluginapi.DataCell{})
	if err = db.importReceipt(ctx, conflict); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	foreign := receipt
	foreign.PluginID = "other.plugin"
	if err = db.importReceipt(ctx, foreign); err == nil {
		t.Fatal("foreign import accepted")
	}
}
func TestCreationBaselineSurvivesRestartAndScopes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	receipt := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	row, err := db.create(ctx, receipt.SourceID, reminder{SessionID: "session-a", Scope: "session", Text: "after two", TriggerJSON: `{"type":"turn_count","n":2}`}, 10)
	if err != nil {
		t.Fatal(err)
	}
	restarted := database{dir: dir}
	for count, want := range map[int]int{10: 0, 11: 0, 12: 1, 13: 1} {
		rows, readErr := restarted.due(ctx, receipt.SourceID, "session-a", "project-a", count, time.Now())
		if readErr != nil || len(rows) != want {
			t.Fatal("restart lost baseline", count, rows, readErr)
		}
	}
	if err = restarted.change(ctx, receipt.SourceID, "session-b", "project-a", row.ID, "ack", ""); !errors.Is(err, errNotFound) {
		t.Fatal("cross-session mutation accepted", err)
	}
	if err = restarted.change(ctx, receipt.SourceID, "session-a", "project-a", row.ID, "scope", "project"); err != nil {
		t.Fatal(err)
	}
	rows, err := restarted.list(ctx, receipt.SourceID, "session-b", "project-a")
	if err != nil || len(rows) != 1 {
		t.Fatal("project scope unavailable", rows, err)
	}
	rows, err = restarted.list(ctx, receipt.SourceID, "session-b", "project-b")
	if err != nil || len(rows) != 0 {
		t.Fatal("project leak", rows, err)
	}
	if err = restarted.change(ctx, receipt.SourceID, "session-b", "project-a", row.ID, "scope", "session"); err != nil {
		t.Fatal(err)
	}
	rows, err = restarted.list(ctx, receipt.SourceID, "session-a", "project-a")
	if err != nil || len(rows) != 1 {
		t.Fatal("demotion lost origin", rows, err)
	}
}
func TestConcurrentConnectionsAndCanceledLock(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	receipt := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	db := database{dir: dir}
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := range 24 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			_, err := (database{dir: dir}).create(ctx, receipt.SourceID, reminder{SessionID: "session-a", Scope: "session", Text: fmt.Sprint(i), TriggerJSON: `{"type":"turn_count","n":2}`}, i)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	rows, err := db.list(ctx, receipt.SourceID, "session-a", "")
	if err != nil || len(rows) != 24 {
		t.Fatal("concurrent writes lost", len(rows), err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.transaction(ctx, receipt.SourceID, func(*table) (bool, error) { close(entered); <-release; return false, nil })
	}()
	<-entered
	canceled, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	err = (database{dir: dir}).change(canceled, receipt.SourceID, "session-a", "", rows[0].ID, "ack", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock ignored cancellation", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestStorageRefusesSymlinkAndCorruption(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sourceFile(receipt.SourceID))
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.list(ctx, receipt.SourceID, "session-a", ""); err == nil {
		t.Fatal("symlink accepted")
	}
	raw, err := os.ReadFile(outside)
	if err != nil || string(raw) != "sentinel" {
		t.Fatal("outside file touched")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`{"import_sha256":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.list(ctx, receipt.SourceID, "session-a", ""); err == nil {
		t.Fatal("corrupt state accepted")
	}
}
func TestStrictTriggers(t *testing.T) {
	for _, raw := range []string{`{"type":"time"}`, `{"type":"time","at":"garbage"}`, `{"type":"turn_count","n":0}`, `{"type":"turn_count","n":-1}`, `{"type":"turn_count","n":1,"n":2}`, `{"type":"turn_count","n":1,"extra":true}`, `{"type":"turn_count","n":1} {}`, `{"type":"time","at":"2026-01-01T00:00:00Z","n":1}`} {
		if _, err := parseTrigger(raw); err == nil {
			t.Fatal("invalid trigger accepted", raw)
		}
	}
}
