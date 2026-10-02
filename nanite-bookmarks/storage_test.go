package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func fixtureExport(t *testing.T, dir, source string, rows [][]pluginapi.DataCell) pluginapi.DataExportReceipt {
	t.Helper()
	snapshot := pluginapi.DataSnapshot{Protocol: 1, PluginID: pluginID, Feature: "bookmarks", SourceID: source, Columns: append(append([]string{}, requiredColumns...), "future_metadata"), Rows: rows}
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
	return pluginapi.DataExportReceipt{PluginID: pluginID, Feature: "bookmarks", SourceID: source, Path: path, SHA256: exported.SHA256, RowCount: len(rows)}
}

func originalRows() [][]pluginapi.DataCell {
	text := func(s string) pluginapi.DataCell { return pluginapi.DataCell{Kind: "text", Text: s} }
	return [][]pluginapi.DataCell{
		{text("b-old"), text("gone-message"), text("session-a"), {Kind: "null"}, text(`["kept","two"]`), text("2025-04-02 12:13:14.987+04:00"), {Kind: "integer", Text: "9223372036854775807"}},
		{text("b-weird"), text("m-other"), text("session-b"), {Kind: "text_bytes", Text: base64.StdEncoding.EncodeToString([]byte{0xff, 0, 0xfe})}, {Kind: "blob", Text: "AP8="}, text("original timestamp verbatim"), {Kind: "real", Text: "0.12345678901234568"}},
	}
}
func readTable(t *testing.T, dir, source string) table {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, sourceFile(source)))
	if err != nil {
		t.Fatal(err)
	}
	var state table
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func TestImportPreservesTypedRowsAndReplayDoesNotResurrect(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	rows := originalRows()
	receipt := fixtureExport(t, dir, "workspace-a", rows)
	if err := db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if got := readTable(t, dir, receipt.SourceID).Snapshot.Rows; !reflect.DeepEqual(got, rows) {
		t.Fatalf("cells changed: %#v", got)
	}
	list, err := db.list(ctx, receipt.SourceID, "session-a")
	if err != nil || len(list) != 1 || list[0].MessageID != "gone-message" {
		t.Fatalf("orphan reference lost: %#v %v", list, err)
	}
	note := "operator edited"
	if err = db.change(ctx, receipt.SourceID, "b-weird", &note); err != nil {
		t.Fatal(err)
	}
	if err = db.change(ctx, receipt.SourceID, "b-old", nil); err != nil {
		t.Fatal(err)
	}
	if err = db.importReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	state := readTable(t, dir, receipt.SourceID)
	if len(state.Snapshot.Rows) != 1 || state.Snapshot.Rows[0][3].Text != note || state.Snapshot.Rows[0][6] != rows[1][6] {
		t.Fatal("replay overwrote operator data")
	}
	// A conflicting committed receipt cannot replace an already imported source.
	changed := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	if err = db.importReceipt(ctx, changed); err == nil {
		t.Fatal("accepted conflicting replay")
	}
	if !reflect.DeepEqual(readTable(t, dir, receipt.SourceID), state) {
		t.Fatal("conflict changed stored data")
	}
}
func TestConcurrentConnectionsAndWorkspaceIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first := fixtureExport(t, dir, "workspace-a", [][]pluginapi.DataCell{})
	second := fixtureExport(t, dir, "workspace-b", [][]pluginapi.DataCell{})
	for _, r := range []pluginapi.DataExportReceipt{first, second} {
		if err := (database{dir: dir}).importReceipt(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := (database{dir: dir}).create(ctx, first.SourceID, pluginapi.CoreReference{SessionID: "session-a", MessageID: fmt.Sprintf("m-%d", i)}, "note", false)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	rows, err := (database{dir: dir}).list(ctx, first.SourceID, "session-a")
	if err != nil || len(rows) != 24 {
		t.Fatalf("lost concurrent writes: %d %v", len(rows), err)
	}
	other, err := (database{dir: dir}).list(ctx, second.SourceID, "session-a")
	if err != nil || len(other) != 0 {
		t.Fatal("workspace rows leaked")
	}
	// A transaction holding the OS lock must block a second connection and let
	// its canceled context exit without publishing anything.
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- (database{dir: dir}).transaction(ctx, first.SourceID, func(*table) (bool, error) { close(entered); <-release; return false, nil })
	}()
	<-entered
	canceled, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	_, _, err = (database{dir: dir}).create(canceled, first.SourceID, pluginapi.CoreReference{SessionID: "session-a", MessageID: "canceled"}, "", false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored cancellation: %v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCorruptionForeignReceiptsAndWriteFailureLeaveData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := database{dir: dir}
	r := fixtureExport(t, dir, "workspace-a", originalRows())
	bad := r
	bad.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := db.importReceipt(ctx, bad); err == nil {
		t.Fatal("accepted checksum mismatch")
	}
	if _, err := os.Stat(filepath.Join(dir, sourceFile(r.SourceID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid import published data")
	}
	bad = r
	bad.PluginID = "nanite.other"
	if err := db.importReceipt(ctx, bad); err == nil {
		t.Fatal("accepted foreign owner")
	}
	if err := db.importReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, sourceFile(r.SourceID)))
	if err != nil {
		t.Fatal(err)
	}
	// Inject a publication failure: replace the state file with a directory.
	if err = os.Remove(filepath.Join(dir, sourceFile(r.SourceID))); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(dir, sourceFile(r.SourceID)), 0700); err != nil {
		t.Fatal(err)
	}
	note := "unsafe"
	if err = db.change(ctx, r.SourceID, "b-old", &note); err == nil {
		t.Fatal("write into non-regular path accepted")
	}
	if err = os.Remove(filepath.Join(dir, sourceFile(r.SourceID))); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, sourceFile(r.SourceID)), before, 0600); err != nil {
		t.Fatal(err)
	}
	// An outside-target symlink is refused and the target stays unchanged.
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, sourceFile(r.SourceID)+".lock")
	if err = os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, lock); err != nil {
		t.Fatal(err)
	}
	if err = db.change(ctx, r.SourceID, "b-old", &note); err == nil {
		t.Fatal("accepted symlink lock")
	}
	after, err := os.ReadFile(outside)
	if err != nil || string(after) != "unchanged" {
		t.Fatal("outside path modified")
	}
	after, err = os.ReadFile(filepath.Join(dir, sourceFile(r.SourceID)))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed mutation changed rows")
	}
}
