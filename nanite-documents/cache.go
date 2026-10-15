package main

import (
	"os"
	"sync"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

// Validated states are immutable. Transactions copy row/cell slices before
// callbacks so a failed mutation cannot poison the cached committed version.
type cacheEntry struct {
	info  os.FileInfo
	state table
}
type tableCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	cleaned map[string]bool
}

func newDatabase(dir string) database {
	return database{dir: dir, cache: &tableCache{entries: map[string]cacheEntry{}, cleaned: map[string]bool{}}}
}
func cloneTable(state table) table {
	state.Snapshot.Columns = append([]string(nil), state.Snapshot.Columns...)
	rows := make([][]pluginapi.DataCell, len(state.Snapshot.Rows))
	for i, row := range state.Snapshot.Rows {
		rows[i] = append([]pluginapi.DataCell(nil), row...)
	}
	state.Snapshot.Rows = rows
	return state
}
func (db database) cached(name string, info os.FileInfo) (table, bool) {
	if db.cache == nil {
		return table{}, false
	}
	db.cache.mu.Lock()
	defer db.cache.mu.Unlock()
	entry, ok := db.cache.entries[name]
	if !ok || !os.SameFile(entry.info, info) || !entry.info.ModTime().Equal(info.ModTime()) || entry.info.Size() != info.Size() {
		return table{}, false
	}
	return cloneTable(entry.state), true
}
func (db database) remember(name string, info os.FileInfo, state table) {
	if db.cache == nil {
		return
	}
	db.cache.mu.Lock()
	defer db.cache.mu.Unlock()
	db.cache.entries[name] = cacheEntry{info: info, state: cloneTable(state)}
}
