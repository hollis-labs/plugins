package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"slices"
	"strings"
)

func (db database) operate(ctx context.Context, source, op string, args map[string]any, session, pid, actor string) (any, error) {
	var result any
	err := db.transactionWithGrowth(ctx, source, func(state *table) (bool, bool, error) {
		if err := validateTable(*state, source); err != nil {
			return false, false, err
		}
		changed, growing, err := apply(state, op, args, session, pid, actor, &result)
		return changed, growing, err
	})
	return result, err
}

// A single transaction spans todos and plans, including approval, cascading
// deletes and batched work sync. Failed batches never partly modify the cache.
func apply(state *table, op string, args map[string]any, session, pid, actor string, result *any) (bool, bool, error) {
	name := "todos"
	if strings.HasPrefix(op, "plan_") {
		name = "plans"
	}
	value := state.Tables[name]
	snapshot := value.Snapshot
	index, _ := columns(snapshot)
	finish := func(changed, growing bool, err error) (bool, bool, error) {
		value.Snapshot = snapshot
		state.Tables[name] = value
		return changed, growing, err
	}
	switch op {
	case "todo_create", "plan_create":
		if len(snapshot.Rows) >= 10000 {
			return false, false, errQuota
		}
		row, err := makeRow(snapshot, args, session, pid, actor)
		if err != nil {
			return false, false, err
		}
		if !strings.HasPrefix(op, "plan_") {
			if parent := getCell(row, index, "parent_id"); parent != "" {
				i, idx, err := find(snapshot, parent)
				if err != nil {
					return false, false, fmt.Errorf("%w: parent todo does not exist", errInvalid)
				}
				if actor == "user" && (getCell(snapshot.Rows[i], idx, "scope") != getCell(row, index, "scope") || getCell(snapshot.Rows[i], idx, "scope_id") != getCell(row, index, "scope_id")) {
					return false, false, fmt.Errorf("%w: parent must share scope", errInvalid)
				}
			}
		}
		snapshot.Rows = append(snapshot.Rows, row)
		*result = project(row, index)
		return finish(true, true, nil)
	case "todo_list", "plan_list":
		scope, scopeID, projectID, err := coordinates(args, session, pid, name == "plans", false)
		if err != nil {
			return false, false, err
		}
		filters := map[string]any{}
		for key, v := range args {
			filters[key] = v
		}
		filters["scope"] = scope
		filters["scope_id"] = scopeID
		if name == "todos" {
			filters["project_id"] = projectID
		}
		*result = ordered(snapshot, filters)
		return false, false, nil
	case "todo_get", "plan_get":
		i, idx, err := find(snapshot, str(args, "id", ""))
		if err != nil {
			return false, false, err
		}
		*result = project(snapshot.Rows[i], idx)
		return false, false, nil
	case "todo_delete", "plan_delete":
		id := str(args, "id", "")
		if id == "" {
			return false, false, errInvalid
		}
		removed := map[string]bool{id: true}
		if name == "todos" {
			children := map[string][]string{}
			for _, row := range snapshot.Rows {
				parent := getCell(row, index, "parent_id")
				if parent != "" {
					children[parent] = append(children[parent], getCell(row, index, "id"))
				}
			}
			queue := []string{id}
			for len(queue) > 0 {
				parent := queue[0]
				queue = queue[1:]
				for _, child := range children[parent] {
					if !removed[child] {
						removed[child] = true
						queue = append(queue, child)
					}
				}
			}
		}

		before := len(snapshot.Rows)
		snapshot.Rows = slices.DeleteFunc(snapshot.Rows, func(row []pluginapi.DataCell) bool { return removed[getCell(row, index, "id")] })
		*result = map[string]string{"deleted": id}
		return finish(before != len(snapshot.Rows), false, nil)
	case "todo_update", "plan_update":
		i, idx, err := find(snapshot, str(args, "id", ""))
		if err != nil {
			return false, false, err
		}
		row := snapshot.Rows[i]
		if name == "plans" && str(args, "step_id", "") != "" {
			return apply(state, "plan_step_update", args, session, pid, actor, result)
		}
		keys := []string{"title", "description", "status", "priority", "labels", "metadata"}
		if name == "plans" {
			keys = []string{"title", "description", "status", "steps", "metadata"}
		}
		growing := false
		for _, key := range keys {
			v, ok := args[key]
			if !ok || v == nil {
				continue
			}
			if actor == "agent" && op != "todo_update" && v == "" {
				continue
			}
			var raw string
			if key == "labels" || key == "metadata" || key == "steps" {
				// Current todo_update stores the supplied string, including empty
				// and malformed legacy JSON, just as core's pointer patch does.
				if op == "todo_update" && actor == "agent" {
					raw, ok = v.(string)
					if !ok {
						return false, false, updateError("invalid", "invalid todo update fields", "")
					}
				} else {
					raw, err = jsonValue(v)
				}
			} else {
				if err = text(v); err == nil {
					raw = v.(string)
				}
			}
			if err != nil {
				return false, false, err
			}
			if op == "todo_update" {
				if key == "status" && !slices.Contains([]string{"pending", "in_progress", "done", "blocked"}, raw) {
					return false, false, updateError("invalid", fmt.Sprintf("invalid status %q", raw), key)
				}
				if key == "priority" && !slices.Contains([]string{"low", "medium", "high", "critical"}, raw) {
					return false, false, updateError("invalid", fmt.Sprintf("invalid priority %q", raw), key)
				}
			}
			if err = boundedField(raw, getCell(row, idx, key)); err != nil {
				return false, false, err
			}
			growing = growing || len(raw) > len(getCell(row, idx, key))
			setCell(row, idx, key, raw)
		}
		if err := validateNative(row, idx, name == "plans"); err != nil {
			return false, false, err
		}
		stamp(row, idx)
		*result = project(row, idx)
		if op == "todo_update" && actor == "agent" {
			*result = todoToolView(row, idx)
		}
		return finish(true, growing, nil)
	case "todo_scope":
		i, idx, err := find(snapshot, str(args, "id", ""))
		if err != nil {
			return false, false, err
		}
		row := snapshot.Rows[i]
		scope := str(args, "scope", "")
		scopeID := str(args, "scope_id", "")
		projectID := str(args, "project_id", "")
		if scope == "project" {
			if projectID == "" {
				projectID = pid
			}
			if scopeID == "" {
				scopeID = projectID
			}
		} else {
			if scopeID == "" {
				scopeID = session
			}
			projectID = ""
		}
		if !slices.Contains([]string{"turn", "session", "project"}, scope) || scopeID == "" || (scope == "project" && projectID == "") {
			return false, false, fmt.Errorf("%w: invalid scope coordinates", errInvalid)
		}
		setCell(row, idx, "scope", scope)
		setCell(row, idx, "scope_id", scopeID)
		row[idx["project_id"]] = pluginapi.DataCell{Kind: "null"}
		if projectID != "" {
			setCell(row, idx, "project_id", projectID)
		}
		stamp(row, idx)
		*result = project(row, idx)
		return finish(true, false, nil)
	case "plan_step_update", "plan_step_add":
		id := str(args, "id", str(args, "plan_id", ""))
		i, idx, err := find(snapshot, id)
		if err != nil {
			return false, false, err
		}
		row := snapshot.Rows[i]
		steps, err := stepsFrom(row, idx)
		if err != nil {
			return false, false, err
		}
		if op == "plan_step_update" {
			found := false
			for _, step := range steps {
				if str(step, "id", "") == str(args, "step_id", "") {
					keys := []string{"title", "status", "todo_id", "notes", "acceptance"}
					if actor == "agent" {
						keys = []string{"status", "notes"}
					}
					for _, key := range keys {
						if v := str(args, key, ""); v != "" {
							if err := text(v); err != nil {
								return false, false, err
							}
							step[key] = v
						}
					}
					found = true
					break
				}
			}
			if !found {
				return false, false, errNotFound
			}
		} else {
			raw, err := jsonValue(args["steps"])
			if err != nil {
				return false, false, err
			}
			var incoming []record
			if err = decodeNumbers(raw, &incoming); err != nil || len(incoming) == 0 {
				return false, false, fmt.Errorf("%w: steps must be a nonempty array", errInvalid)
			}
			seen := map[string]bool{}
			for _, step := range steps {
				seen[str(step, "id", "")] = true
			}
			appended := []record{}
			for _, step := range incoming {
				if step == nil || str(step, "title", "") == "" {
					return false, false, fmt.Errorf("%w: step title required", errInvalid)
				}
				id := str(step, "id", "")
				if id == "" {
					id = nextStepID(steps, appended)
					step["id"] = id
				}
				if seen[id] {
					return false, false, fmt.Errorf("%w: step ID collides", errInvalid)
				}
				seen[id] = true
				if str(step, "status", "") == "" {
					step["status"] = "pending"
				}
				appended = append(appended, step)
			}
			steps = append(steps, appended...)
			*result = map[string]any{"plan_id": id, "appended": appended, "appended_count": len(appended)}
		}
		oldLen := len(getCell(row, idx, "steps"))
		if err := encodeSteps(row, idx, steps); err != nil {
			return false, false, err
		}
		if op == "plan_step_update" {
			*result = project(row, idx)
		}
		return finish(true, len(getCell(row, idx, "steps")) > oldLen, nil)
	case "plan_approve":
		i, idx, err := find(snapshot, str(args, "id", ""))
		if err != nil {
			return false, false, err
		}
		row := snapshot.Rows[i]
		if getCell(row, idx, "status") != "proposed" {
			return false, false, fmt.Errorf("%w: plan must be proposed to approve", errInvalid)
		}
		createTodos, _ := args["create_todos"].(bool)
		if createTodos {
			steps, err := stepsFrom(row, idx)
			if err != nil {
				return false, false, err
			}
			for _, step := range steps {
				if str(step, "title", "") == "" {
					continue
				}
				req := map[string]any{"title": str(step, "title", ""), "description": str(step, "acceptance", ""), "scope": getCell(row, idx, "scope"), "scope_id": getCell(row, idx, "scope_id")}
				if req["scope"] == "project" {
					req["project_id"] = req["scope_id"]
				}
				var created any
				if _, _, err := apply(state, "todo_create", req, session, pid, getCell(row, idx, "created_by"), &created); err != nil {
					return false, false, err
				}
				step["todo_id"] = created.(record)["id"]
			}
			if err := encodeSteps(row, idx, steps); err != nil {
				return false, false, err
			}
		}
		setCell(row, idx, "status", "approved")
		stamp(row, idx)
		*result = project(row, idx)
		return finish(true, createTodos, nil)
	case "work_reorder":
		items, ok := args["items"].([]any)
		if !ok || len(items) > 10000 {
			return false, false, errInvalid
		}
		for _, item := range items {
			values, ok := item.(map[string]any)
			if !ok {
				return false, false, errInvalid
			}
			order, ok := values["sort_order"].(float64)
			if n, isNumber := values["sort_order"].(json.Number); isNumber {
				var parseErr error
				order, parseErr = n.Float64()
				ok = parseErr == nil
			}
			if !ok || order < 0 || order > 10000 || order != float64(int(order)) {
				return false, false, errInvalid
			}
			i, idx, err := find(snapshot, str(values, "id", ""))
			if err != nil {
				return false, false, err
			}
			row := snapshot.Rows[i]
			var metadata map[string]any
			if err = decodeNumbers(getCell(row, idx, "metadata"), &metadata); err != nil {
				metadata = nil
			}
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata["sort_order"] = order
			raw, _ := json.Marshal(metadata)
			if err := boundedField(string(raw), getCell(row, idx, "metadata")); err != nil {
				return false, false, err
			}
			setCell(row, idx, "metadata", string(raw))
			stamp(row, idx)
		}
		*result = map[string]bool{"ok": true}
		return finish(true, true, nil)

	case "work_sync":
		skipped := []map[string]any{}
		for _, key := range []string{"todos_checked", "todos_unchecked", "plan_steps_checked", "plan_steps_unchecked"} {
			entries, ok := args[key].([]any)
			if !ok && args[key] != nil {
				return false, false, errInvalid
			}
			for _, entry := range entries {
				status := "done"
				if strings.Contains(key, "unchecked") {
					status = "pending"
				}
				operation := "todo_update"
				req := map[string]any{"status": status}
				if strings.HasPrefix(key, "plan_") {
					item, ok := entry.(map[string]any)
					if !ok {
						return false, false, errInvalid
					}
					operation = "plan_step_update"
					req["id"] = item["plan_id"]
					req["step_id"] = item["step_id"]
				} else if key == "todos_checked" {
					req["id"] = entry
				} else {
					item, ok := entry.(map[string]any)
					if !ok {
						return false, false, errInvalid
					}
					req["id"] = item["id"]
				}
				if str(req, "id", "") == "" || (operation == "plan_step_update" && str(req, "step_id", "") == "") {
					return false, false, errInvalid
				}
				var ignored any
				if _, _, err := apply(state, operation, req, session, pid, actor, &ignored); err != nil {
					if errors.Is(err, errNotFound) {
						skipped = append(skipped, req)
						continue
					}
					return false, false, err
				}
			}
		}
		*result = map[string]any{"ok": true, "skipped": skipped}
		return true, false, nil
	}
	return false, false, fmt.Errorf("%w: unknown operation", errInvalid)
}
