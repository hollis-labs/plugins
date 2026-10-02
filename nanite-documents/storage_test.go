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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

func fixtureExport(t *testing.T, dir, source string, rows [][]pluginapi.DataCell) pluginapi.DataExportReceipt {
	t.Helper()
	snapshot := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: "documents", SourceID: source, Columns: append(append([]string{}, requiredColumns...), "future_metadata"), Rows: rows}
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
	return pluginapi.DataExportReceipt{PluginID: pluginID, Feature: "documents", SourceID: source, Path: path, SHA256: exported.SHA256, RowCount: len(rows)}
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
		{cell("old-full"), cell("session-a"), cell("Full"), cell("text/plain"), cell("retained full content"), {Kind: "integer", Text: "21"}, boolCell(true), boolCell(true), cell("kept summary"), cell("original created"), cell("original updated"), {Kind: "integer", Text: "9223372036854775807"}},
		{cell("old-pointer"), cell("session-a"), cell("Pointer"), cell("text/plain"), cell("hidden pointer content"), {Kind: "integer", Text: "999"}, boolCell(true), boolCell(false), cell("only summary"), cell("2025-01-01"), cell("original updated"), {Kind: "blob", Text: "AP8="}},
		{cell("excluded"), cell("session-a"), cell("Excluded"), cell("text/plain"), cell("excluded content"), {Kind: "integer", Text: "16"}, boolCell(false), boolCell(true), cell(""), cell("2025-01-02"), cell("original updated"), {Kind: "real", Text: "0.12345678901234568"}},
		{cell("orphan"), cell("orphan-session"), cell("Legacy"), cell("text/plain"), {Kind: "text_bytes", Text: base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0xfe})}, {Kind: "integer", Text: "3"}, boolCell(true), boolCell(true), {Kind: "null"}, cell("original created"), cell("original updated"), {Kind: "blob", Text: "AAH/"}},
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
	summary := "edited summary"
	if err := db.change(ctx, receipt.SourceID, "session-a", "old-full", patchRequest{Summary: &summary}, false); err != nil {
		t.Fatal(err)
	}
	if err := db.change(ctx, receipt.SourceID, "session-a", "old-pointer", patchRequest{}, true); err != nil {
		t.Fatal(err)
	}
	after := readTable(t, dir, receipt.SourceID)
	for i, c := range original[0] {
		if i != 8 && i != 10 && after.Snapshot.Rows[0][i] != c {
			t.Fatal("unrelated cell changed", i)
		}
	}
	if len(after.Snapshot.Rows) != 3 || !reflect.DeepEqual(after.Snapshot.Rows[2], original[3]) {
		t.Fatal("orphan lost")
	}
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID), after) {
		t.Fatal("replay restored edits/deletes")
	}
	conflict := fixtureExport(t, dir, receipt.SourceID, [][]pluginapi.DataCell{})
	if err := db.importReceipt(ctx, conflict); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	foreign := receipt
	foreign.PluginID = "other.plugin"
	if err := db.importReceipt(ctx, foreign); err == nil {
		t.Fatal("foreign receipt accepted")
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
			_, err := (database{dir: dir}).create(ctx, receipt.SourceID, document{SessionID: session, Name: fmt.Sprint(i), Content: fmt.Sprint(i)})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	rows, err := db.list(ctx, receipt.SourceID, "session-a")
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
	err = (database{dir: dir}).change(canceled, receipt.SourceID, "session-a", rows[0].ID, patchRequest{}, true)
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
	if _, err := db.list(ctx, receipt.SourceID, "session-a"); err == nil {
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
	if _, err = db.list(ctx, receipt.SourceID, "session-a"); err == nil {
		t.Fatal("corrupt storage accepted")
	}
}

func TestContentChunksPaginationAndLimits(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	receipt := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("a", 65535) + "界" + "tail"
	d, err := db.create(ctx, receipt.SourceID, document{SessionID: "session-a", Name: "chunked", Content: content, SizeBytes: int64(len(content))})
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.read(ctx, receipt.SourceID, "session-a", d.ID, 0, 65536)
	if err != nil || first.NextOffset != 65535 || !first.More {
		t.Fatal(first.NextOffset, err)
	}
	second, err := db.read(ctx, receipt.SourceID, "session-a", d.ID, first.NextOffset, 65536)
	if err != nil || first.Document.Content+second.Document.Content != content {
		t.Fatal("chunked read changed content", err)
	}
	if _, err = db.read(ctx, receipt.SourceID, "session-b", d.ID, 0, 0); !errors.Is(err, errNotFound) {
		t.Fatal("cross-session read")
	}
	if _, err = db.read(ctx, receipt.SourceID, "session-a", d.ID, 65536, 0); err == nil {
		t.Fatal("mid-rune offset accepted")
	}
	if _, err = db.read(ctx, receipt.SourceID, "session-a", d.ID, 0, 65537); err == nil {
		t.Fatal("chunk limit bypass")
	}
	for i := range 101 {
		if _, err = db.create(ctx, receipt.SourceID, document{SessionID: "session-a", Name: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.page(ctx, receipt.SourceID, "session-a", 0)
	if err != nil || len(page.Documents) != 100 || !page.More || page.NextOffset != 100 {
		t.Fatal("page", page, err)
	}
	page, err = db.page(ctx, receipt.SourceID, "session-a", page.NextOffset)
	if err != nil || len(page.Documents) != 2 || page.More {
		t.Fatal("last page", page, err)
	}
	if err := validateCreate(createRequest{Name: "empty file", Content: ""}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []createRequest{{Name: "bad", Content: strings.Repeat("界", (maxContentBytes/3)+1)}, {Name: strings.Repeat("x", 513)}, {Name: "doc", Summary: strings.Repeat("x", maxSummaryBytes+1)}, {Name: "doc", Content: string([]byte{0xff})}} {
		if err := validateCreate(req); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
}
func TestReceiptChecksumAndMissingColumns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	receipt := fixtureExport(t, dir, "workspace-a", legacyRows())
	bad := receipt
	bad.SHA256 = strings.Repeat("0", 64)
	if err := db.importReceipt(ctx, bad); err == nil {
		t.Fatal("checksum bypass")
	}
	bad = receipt
	bad.RowCount++
	if err := db.importReceipt(ctx, bad); err == nil {
		t.Fatal("row count bypass")
	}
	if _, err := os.Stat(filepath.Join(dir, sourceFile(receipt.SourceID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed import created state")
	}
	snapshot := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: "documents", SourceID: "workspace-a", Columns: []string{"id"}, Rows: [][]pluginapi.DataCell{{cell("one")}}}
	if err := validateTable(table{ImportSHA256: receipt.SHA256, Snapshot: snapshot}, receipt.SourceID); err == nil {
		t.Fatal("missing columns accepted")
	}
}
