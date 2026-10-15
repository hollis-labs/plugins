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
	snapshot := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: "pins", SourceID: source, Columns: append(append([]string{}, requiredColumns...), "future_metadata"), Rows: rows}
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
	return pluginapi.DataExportReceipt{PluginID: pluginID, Feature: "pins", SourceID: source, Path: path, SHA256: exported.SHA256, RowCount: len(rows)}
}
func cell(text string) pluginapi.DataCell { return pluginapi.DataCell{Kind: "text", Text: text} }
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
func legacyRows() [][]pluginapi.DataCell {
	return [][]pluginapi.DataCell{
		{cell("old-session"), cell("session-a"), cell("session"), {Kind: "null"}, cell("retained pin"), cell("original-agent"), cell("original created"), cell("original updated"), {Kind: "integer", Text: "9223372036854775807"}},
		{cell("old-project"), {Kind: "null"}, cell("project"), cell("project-a"), cell("project pin"), cell("original-agent"), cell("2025-01-01"), cell("original updated"), {Kind: "blob", Text: "AP8="}},
		{cell("legacy-turn"), cell("session-a"), cell("turn"), {Kind: "null"}, cell("turn pin is retained"), {Kind: "null"}, cell("2025-01-01"), cell("original updated"), {Kind: "real", Text: "0.12345678901234568"}},
		{cell("weird-pin"), cell("orphan-session"), cell("session"), {Kind: "null"}, {Kind: "text_bytes", Text: base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0xfe})}, {Kind: "null"}, cell("original created"), cell("original updated"), {Kind: "blob", Text: "AAH/"}},
	}
}
func TestImportTypedRowsAndReplayPreservesEditsDeletes(t *testing.T) {
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
	if err := db.change(ctx, receipt.SourceID, "session-a", "project-a", "old-session", "content", "operator edit"); err != nil {
		t.Fatal(err)
	}
	if err := db.change(ctx, receipt.SourceID, "session-a", "project-a", "old-project", "delete", ""); err != nil {
		t.Fatal(err)
	}
	after := readTable(t, dir, receipt.SourceID)
	if len(after.Snapshot.Rows) != 3 || after.Snapshot.Rows[0][4].Text != "operator edit" || after.Snapshot.Rows[0][5] != original[0][5] || after.Snapshot.Rows[0][6] != original[0][6] || after.Snapshot.Rows[0][8] != original[0][8] || !reflect.DeepEqual(after.Snapshot.Rows[2], original[3]) {
		t.Fatal("mutation lost unrelated cells")
	}
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID), after) {
		t.Fatal("replay resurrected edits/deletions")
	}
	conflict := fixtureExport(t, dir, receipt.SourceID, [][]pluginapi.DataCell{})
	if err := db.importReceipt(ctx, conflict); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	foreign := receipt
	foreign.PluginID = "other.plugin"
	if err := db.importReceipt(ctx, foreign); err == nil {
		t.Fatal("foreign import accepted")
	}
}
func TestProjectScopeAndNullOriginDemotion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	rows, err := db.list(ctx, receipt.SourceID, "session-b", "project-a")
	if err != nil || len(rows) != 1 || rows[0].SessionID != nil {
		t.Fatal("null-origin project pin lost", rows, err)
	}
	if err = db.change(ctx, receipt.SourceID, "session-b", "project-a", "old-project", "scope", "session"); err == nil {
		t.Fatal("demotion invented origin")
	}
	if err = db.change(ctx, receipt.SourceID, "session-b", "project-b", "old-project", "delete", ""); !errors.Is(err, errNotFound) {
		t.Fatal("cross-project deletion accepted", err)
	}
	if err = db.change(ctx, receipt.SourceID, "session-a", "project-a", "old-session", "scope", "project"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.list(ctx, receipt.SourceID, "session-b", "project-a")
	if err != nil || len(rows) != 2 {
		t.Fatal("project pin not shared", rows, err)
	}
	if err = db.change(ctx, receipt.SourceID, "session-b", "project-a", "old-session", "scope", "session"); err != nil {
		t.Fatal(err)
	}
	rows, err = db.list(ctx, receipt.SourceID, "session-b", "project-a")
	if err != nil || len(rows) != 1 {
		t.Fatal("demotion did not restore origin", rows, err)
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
			session := "session-a"
			_, err := (database{dir: dir}).create(ctx, receipt.SourceID, pin{SessionID: &session, Scope: "session", Content: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	rows, err := db.list(ctx, receipt.SourceID, "session-a", "")
	if err != nil || len(rows) != 24 {
		t.Fatal("concurrent write lost", len(rows), err)
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
	err = (database{dir: dir}).change(canceled, receipt.SourceID, "session-a", "", rows[0].ID, "delete", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled lock accepted", err)
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
		t.Fatal("outside file changed")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(`{"import_sha256":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = db.list(ctx, receipt.SourceID, "session-a", ""); err == nil {
		t.Fatal("corrupt storage accepted")
	}
}
