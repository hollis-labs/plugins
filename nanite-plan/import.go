package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

var requiredColumns = map[string][]string{
	"todos": {"id", "scope", "scope_id", "project_id", "parent_id", "title", "description", "status", "priority", "labels", "metadata", "created_by", "created_at", "updated_at"},
	"plans": {"id", "scope", "scope_id", "title", "description", "status", "steps", "metadata", "created_by", "created_at", "updated_at"},
}

func columns(snapshot pluginapi.DataSnapshot) (map[string]int, error) {
	index := map[string]int{}
	for i, name := range snapshot.Columns {
		index[name] = i
	}
	for _, name := range requiredColumns[snapshot.Feature] {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("plan: required column %s absent", name)
		}
	}
	if _, ok := index["id"]; !ok {
		return nil, fmt.Errorf("plan: id column absent")
	}
	return index, nil
}

func validateTable(state table, source string) error {
	for _, name := range []string{"todos", "plans"} {
		if _, ok := state.Tables[name]; !ok {
			return fmt.Errorf("plan: both committed tables required")
		}
	}
	for name, value := range state.Tables {
		if name != "todos" && name != "plans" && name != "todo-legacy" {
			return fmt.Errorf("plan: unknown table")
		}
		if len(value.ImportSHA256) != 64 || value.Snapshot.SourceID != source || value.Snapshot.PluginID != pluginID || value.Snapshot.Feature != name {
			return fmt.Errorf("plan: storage identity differs")
		}
		if _, err := hex.DecodeString(value.ImportSHA256); err != nil {
			return err
		}
		if err := value.Snapshot.Validate(); err != nil {
			return err
		}
		index, err := columns(value.Snapshot)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, row := range value.Snapshot.Rows {
			id, err := cellString(row[index["id"]])
			if err != nil || id == "" || seen[id] {
				return fmt.Errorf("plan: missing or duplicate row ID")
			}
			seen[id] = true
		}
	}
	return nil
}

// Both tables and their checkpoints commit together, including empty tables.
// Matching replay uses only durable state, so exports may be removed afterwards.
// Optional todo-legacy archives retired migration-043 rows without reactivating them.
func (db database) importReceipts(ctx context.Context, receipts []pluginapi.DataExportReceipt) (string, error) {
	source := ""
	selected := map[string]pluginapi.DataExportReceipt{}
	for _, receipt := range receipts {
		if receipt.Feature != "todos" && receipt.Feature != "plans" && receipt.Feature != "todo-legacy" {
			continue
		}
		if receipt.PluginID != pluginID || receipt.SourceID == "" {
			return "", fmt.Errorf("plan: foreign receipt")
		}
		if source != "" && receipt.SourceID != source {
			return "", fmt.Errorf("plan: ambiguous workspace receipts")
		}
		if _, exists := selected[receipt.Feature]; exists {
			return "", fmt.Errorf("plan: duplicate receipt")
		}
		selected[receipt.Feature] = receipt
		source = receipt.SourceID
	}
	if _, ok := selected["todos"]; !ok {
		return "", fmt.Errorf("%w: waiting for committed todos import", errUnavailable)
	}
	if _, ok := selected["plans"]; !ok {
		return "", fmt.Errorf("%w: waiting for committed plans import", errUnavailable)
	}
	err := db.transaction(ctx, source, func(state *table) (bool, error) {
		if state.Tables == nil {
			state.Tables = map[string]importedTable{}
		}
		changed := false
		root, err := os.OpenRoot(db.dir)
		if err != nil {
			return false, err
		}
		defer root.Close() //nolint:errcheck // No write buffers.
		for name, receipt := range selected {
			if existing, ok := state.Tables[name]; ok {
				if existing.ImportSHA256 != receipt.SHA256 {
					return false, fmt.Errorf("plan: conflicting export replay")
				}
				continue
			}
			file, err := root.Open(receipt.Path)
			if err != nil {
				return false, err
			}
			exported, decodeErr := pluginapi.DecodeDataExport(file)
			closeErr := file.Close()
			if decodeErr != nil {
				return false, decodeErr
			}
			if closeErr != nil {
				return false, closeErr
			}
			if err := receipt.Verify(exported); err != nil {
				return false, err
			}
			state.Tables[name] = importedTable{ImportSHA256: receipt.SHA256, Snapshot: exported.Snapshot}
			changed = true
		}
		return changed, nil
	})
	return source, err
}

func cellString(cell pluginapi.DataCell) (string, error) {
	value, err := cell.Value()
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return fmt.Sprint(v), nil
	}
}
