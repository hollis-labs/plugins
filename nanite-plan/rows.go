package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
)

type record map[string]any

func str(args map[string]any, key, fallback string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return fallback
}
func text(value any) error {
	s, ok := value.(string)
	if !ok || !utf8.ValidString(s) || strings.ContainsRune(s, 0) || len(s) > 65536 {
		return fmt.Errorf("%w: field must be UTF-8 text of at most 64 KiB without NUL", errInvalid)
	}
	return nil
}
func jsonValue(value any) (string, error) {
	if s, ok := value.(string); ok {
		if !json.Valid([]byte(s)) {
			return "", fmt.Errorf("%w: malformed JSON field", errInvalid)
		}
		return s, nil
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}
func project(row []pluginapi.DataCell, index map[string]int) record {
	result := record{}
	for name, i := range index {
		known := slices.Contains(requiredColumns["todos"], name) || slices.Contains(requiredColumns["plans"], name)
		if !known {
			continue
		}
		if row[i].Kind == "null" {
			if name == "project_id" {
				continue
			}
			result[name] = ""
			continue
		}
		value, _ := cellString(row[i])
		result[name] = value
		switch name {
		case "steps", "labels":
			var parsed []any
			_ = json.Unmarshal([]byte(value), &parsed)
			if parsed == nil {
				parsed = []any{}
			}
			result[name] = parsed
		case "metadata":
			var parsed map[string]any
			_ = json.Unmarshal([]byte(value), &parsed)
			if parsed == nil {
				parsed = map[string]any{}
			}
			result[name] = parsed
		}
	}
	return result
}
func getCell(row []pluginapi.DataCell, index map[string]int, key string) string {
	i, ok := index[key]
	if !ok {
		return ""
	}
	value, _ := cellString(row[i])
	return value
}
func setCell(row []pluginapi.DataCell, index map[string]int, key, value string) {
	if i, ok := index[key]; ok {
		row[i] = pluginapi.DataCell{Kind: "text", Text: value}
	}
}
func stamp(row []pluginapi.DataCell, index map[string]int) {
	setCell(row, index, "updated_at", time.Now().UTC().Format(time.RFC3339Nano))
}

// Caller coordinates default exactly as the core tools. Explicit scope IDs
// remain supported: the approved core surface was workspace-wide, not private
// to the invoking session. Query grants authorize session metadata lookups.
func coordinates(args map[string]any, session, projectID string, plan, create bool) (string, string, string, error) {
	fallback := ""
	if create && !plan {
		fallback = "session"
	}
	scope := str(args, "scope", fallback)
	scopeID := str(args, "scope_id", "")
	pid := str(args, "project_id", "")
	if create && plan && scope == "" {
		return "", "", "", fmt.Errorf("%w: title and scope are required", errInvalid)
	}
	if scope != "" && ((!plan && !slices.Contains([]string{"turn", "session", "project"}, scope)) || (plan && !slices.Contains([]string{"workspace", "session", "project"}, scope))) {
		return "", "", "", fmt.Errorf("%w: invalid scope", errInvalid)
	}
	if !plan && scope == "project" {
		if pid == "" {
			pid = projectID
		}
		if create && pid == "" {
			return "", "", "", fmt.Errorf("%w: project scope requires project_id", errInvalid)
		}
		if scopeID == "" {
			scopeID = pid
		}
	} else if scopeID == "" && (scope == "session" || (!plan && scope == "turn") || (create && plan && scope == "project")) {
		scopeID = session
	}
	if create && scope != "workspace" && scopeID == "" {
		return "", "", "", fmt.Errorf("%w: scope_id required without current session", errInvalid)
	}
	return scope, scopeID, pid, nil
}

func makeRow(snapshot pluginapi.DataSnapshot, args map[string]any, session, pid, actor string) ([]pluginapi.DataCell, error) {
	isPlan := snapshot.Feature == "plans"
	scope, scopeID, projectID, err := coordinates(args, session, pid, isPlan, true)
	if err != nil {
		return nil, err
	}
	title := str(args, "title", "")
	if title == "" {
		return nil, fmt.Errorf("%w: title required", errInvalid)
	}
	index, _ := columns(snapshot)
	row := make([]pluginapi.DataCell, len(snapshot.Columns))
	for i := range row {
		row[i] = pluginapi.DataCell{Kind: "null"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fields := map[string]string{"id": newID(), "title": title, "description": str(args, "description", ""), "scope": scope, "scope_id": scopeID, "created_by": actor, "created_at": now, "updated_at": now, "metadata": "{}"}
	if isPlan {
		fields["status"] = str(args, "status", "proposed")
		fields["steps"] = str(args, "steps", "[]")
	} else {
		fields["status"] = str(args, "status", "pending")
		fields["priority"] = str(args, "priority", "medium")
		fields["labels"] = str(args, "labels", "[]")
		if projectID != "" {
			fields["project_id"] = projectID
		}
		if parent := str(args, "parent_id", ""); parent != "" {
			fields["parent_id"] = parent
		}
	}
	for _, key := range []string{"steps", "labels", "metadata"} {
		if value, ok := args[key]; ok {
			raw, err := jsonValue(value)
			if err != nil {
				return nil, err
			}
			fields[key] = raw
		}
	}
	for key, value := range fields {
		if err := text(value); err != nil {
			return nil, err
		}
		setCell(row, index, key, value)
	}
	if err := validateNative(row, index, isPlan); err != nil {
		return nil, err
	}
	return row, nil
}
func validateNative(row []pluginapi.DataCell, index map[string]int, plan bool) error {
	status := getCell(row, index, "status")
	if plan {
		if !slices.Contains([]string{"proposed", "approved", "in_progress", "complete", "abandoned"}, status) {
			return fmt.Errorf("%w: invalid plan status", errInvalid)
		}
	} else {
		if !slices.Contains([]string{"pending", "in_progress", "done", "blocked"}, status) || !slices.Contains([]string{"low", "medium", "high", "critical"}, getCell(row, index, "priority")) {
			return fmt.Errorf("%w: invalid todo status/priority", errInvalid)
		}
	}
	return nil
}
func find(snapshot pluginapi.DataSnapshot, id string) (int, map[string]int, error) {
	index, _ := columns(snapshot)
	for i, row := range snapshot.Rows {
		if getCell(row, index, "id") == id {
			return i, index, nil
		}
	}
	return -1, index, errNotFound
}
func rowMatches(row []pluginapi.DataCell, index map[string]int, args map[string]any) bool {
	for _, key := range []string{"scope", "scope_id", "status", "priority", "parent_id"} {
		value := str(args, key, "")
		if value == "" {
			continue
		}
		got := getCell(row, index, key)
		if key == "parent_id" && value == "*" {
			if got == "" {
				return false
			}
			continue
		}
		if got != value {
			return false
		}
	}
	if pid := str(args, "project_id", ""); pid != "" {
		if getCell(row, index, "project_id") != pid {
			return false
		}
	}
	if labels := str(args, "labels", ""); labels != "" {
		var stored []string
		_ = json.Unmarshal([]byte(getCell(row, index, "labels")), &stored)
		match := false
		for _, label := range strings.Split(labels, ",") {
			if slices.Contains(stored, strings.TrimSpace(label)) {
				match = true
			}
		}
		if !match {
			return false
		}
	}
	return true
}
func ordered(snapshot pluginapi.DataSnapshot, args map[string]any) []record {
	result := []record{}
	index, _ := columns(snapshot)
	for _, row := range snapshot.Rows {
		if rowMatches(row, index, args) {
			result = append(result, project(row, index))
		}
	}
	slices.SortStableFunc(result, func(a, b record) int {
		if c := strings.Compare(str(b, "created_at", ""), str(a, "created_at", "")); c != 0 {
			return c
		}
		return strings.Compare(str(a, "id", ""), str(b, "id", ""))
	})
	return result
}
func stepsFrom(row []pluginapi.DataCell, index map[string]int) ([]record, error) {
	var steps []record
	err := json.Unmarshal([]byte(getCell(row, index, "steps")), &steps)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid steps JSON", errInvalid)
	}
	if steps == nil {
		steps = []record{}
	}
	return steps, nil
}
func encodeSteps(row []pluginapi.DataCell, index map[string]int, steps []record) error {
	raw, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return fmt.Errorf("%w: steps exceed 64 KiB", errQuota)
	}
	setCell(row, index, "steps", string(raw))
	stamp(row, index)
	return nil
}
func nextStepID(existing, pending []record) string {
	maxN := 0
	for _, steps := range [][]record{existing, pending} {
		for _, step := range steps {
			id := str(step, "id", "")
			if strings.HasPrefix(id, "s") {
				n, err := strconv.Atoi(id[1:])
				if err == nil && n > maxN {
					maxN = n
				}
			}
		}
	}
	if maxN > 0 && maxN < 1000000000 {
		return fmt.Sprintf("s%d", maxN+1)
	}
	return newID()
}
