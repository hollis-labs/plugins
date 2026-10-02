package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func httpStatus(err error) int {
	switch {
	case errors.Is(err, errNotFound):
		return http.StatusNotFound
	case errors.Is(err, errInvalid):
		return http.StatusBadRequest
	case errors.Is(err, errQuota):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, errUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
func (p *planPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	mux := http.NewServeMux()
	handler := func(op string, body, listing bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			operation := op
			send := func(status int, value any) {
				raw, err := json.Marshal(value)
				if err != nil {
					http.Error(w, "response encoding failed", http.StatusInternalServerError)
					return
				}
				if len(raw) > maxResponseBytes {
					http.Error(w, "response exceeds 2 MiB", http.StatusRequestEntityTooLarge)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(status)
				_, _ = w.Write(raw)
			}
			q := r.URL.Query()
			ids := q["session_id"]
			if len(ids) != 1 || ids[0] == "" || (call.SessionID != "" && call.SessionID != ids[0]) {
				http.Error(w, "one calling session_id required", http.StatusBadRequest)
				return
			}
			source, err := p.ready(r.Context())
			if err != nil {
				http.Error(w, err.Error(), httpStatus(err))
				return
			}
			meta, err := p.session(r.Context(), ids[0])
			if err != nil {
				http.Error(w, err.Error(), httpStatus(err))
				return
			}
			args := map[string]any{}
			if body {
				raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
				if err != nil {
					http.Error(w, "request exceeds 512 KiB", http.StatusRequestEntityTooLarge)
					return
				}
				var wrapped struct {
					Request map[string]any `json:"request"`
				}
				envelope := append([]byte(`{"request":`), raw...)
				envelope = append(envelope, '}')
				if err = manifest.DecodeExtension(envelope, &wrapped); err != nil || wrapped.Request == nil {
					http.Error(w, "invalid request object", http.StatusBadRequest)
					return
				}
				args = wrapped.Request
				if err = checkHTTPArguments(operation, args); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			if id := r.PathValue("id"); id != "" {
				args["id"] = id
			}
			if step := r.PathValue("stepID"); step != "" {
				args["step_id"] = step
			}
			if listing {
				for _, key := range []string{"scope", "scope_id", "project_id", "status", "priority", "parent_id", "labels"} {
					if len(q[key]) > 1 {
						http.Error(w, "duplicate filter", http.StatusBadRequest)
						return
					}
					if q.Has(key) {
						args[key] = q.Get(key)
					}
				}
			}
			if operation == "todo_children" {
				args["parent_id"] = r.PathValue("id")
				operation = "todo_list"
			}
			p.mu.Lock()
			db := p.db
			p.mu.Unlock()
			value, err := db.operate(r.Context(), source, operation, args, meta.ID, meta.ProjectID, "user")
			if err != nil {
				http.Error(w, err.Error(), httpStatus(err))
				return
			}
			if listing {
				offset := 0
				if len(q["offset"]) > 1 {
					http.Error(w, "duplicate offset", http.StatusBadRequest)
					return
				}
				if q.Has("offset") {
					offset, err = strconv.Atoi(q.Get("offset"))
					if err != nil || offset < 0 || offset > pluginapi.MaxDataExportRows {
						http.Error(w, "invalid offset", http.StatusBadRequest)
						return
					}
				}
				rows := value.([]record)
				start := min(offset, len(rows))
				end := min(start+100, len(rows))
				name := "todos"
				if operation == "plan_list" {
					name = "plans"
				}
				// Pack whole records. A single oversized legacy record remains durable
				// but is explicitly refused by the bounded public transport.
				for end > start {
					trial, _ := json.Marshal(rows[start:end])
					if len(trial) <= maxResponseBytes-1024 {
						break
					}
					end--
				}
				if end == start && start < len(rows) {
					http.Error(w, "legacy work item exceeds response limit", http.StatusRequestEntityTooLarge)
					return
				}
				send(200, map[string]any{name: rows[start:end], "more": end < len(rows), "next_offset": end, "project_id": meta.ProjectID})
				return
			}
			status := 200
			if operation == "todo_create" || operation == "plan_create" {
				status = 201
			}
			send(status, value)
		}
	}
	mux.HandleFunc("GET /todos", handler("todo_list", false, true))
	mux.HandleFunc("POST /todos", handler("todo_create", true, false))
	mux.HandleFunc("GET /todos/{id}", handler("todo_get", false, false))
	mux.HandleFunc("PUT /todos/{id}", handler("todo_update", true, false))
	mux.HandleFunc("DELETE /todos/{id}", handler("todo_delete", false, false))
	mux.HandleFunc("GET /todos/{id}/children", handler("todo_children", false, true))
	mux.HandleFunc("PATCH /todos/{id}/scope", handler("todo_scope", true, false))
	mux.HandleFunc("GET /plans", handler("plan_list", false, true))
	mux.HandleFunc("POST /plans", handler("plan_create", true, false))
	mux.HandleFunc("GET /plans/{id}", handler("plan_get", false, false))
	mux.HandleFunc("PUT /plans/{id}", handler("plan_update", true, false))
	mux.HandleFunc("DELETE /plans/{id}", handler("plan_delete", false, false))
	mux.HandleFunc("PUT /plans/{id}/steps/{stepID}", handler("plan_step_update", true, false))
	mux.HandleFunc("POST /plans/{id}/approve", handler("plan_approve", true, false))
	mux.HandleFunc("POST /plans/{id}/steps", handler("plan_step_add", true, false))
	mux.HandleFunc("POST /work/reorder", handler("work_reorder", true, false))
	mux.HandleFunc("POST /work/sync", handler("work_sync", true, false))
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}
func checkHTTPArguments(op string, args map[string]any) error {
	allowed := map[string][]string{
		"todo_create": {"scope", "scope_id", "project_id", "parent_id", "title", "description", "status", "priority", "labels", "metadata"},
		"plan_create": {"scope", "scope_id", "title", "description", "status", "steps", "metadata"},
		"todo_update": {"title", "description", "status", "priority", "labels", "metadata"}, "plan_update": {"title", "description", "status", "steps", "metadata"},
		"plan_step_add": {"steps"}, "work_reorder": {"items"},
		"todo_scope": {"scope", "scope_id", "project_id"}, "plan_step_update": {"title", "status", "todo_id", "notes", "acceptance", "depends_on"},
		"plan_approve": {"create_todos"}, "work_sync": {"todos_checked", "todos_unchecked", "todos_added", "todos_reordered", "plan_steps_checked", "plan_steps_unchecked", "plans_approved", "plans_rejected"},
	}
	for key, value := range args {
		if !slices.Contains(allowed[op], key) || value == nil {
			return fmt.Errorf("%w: unknown/null field %s", errInvalid, key)
		}
		switch key {
		case "create_todos", "todos_reordered":
			if _, ok := value.(bool); !ok {
				return errInvalid
			}
		case "labels", "steps", "depends_on":
			if _, ok := value.([]any); !ok {
				return errInvalid
			}
		case "metadata":
			if _, ok := value.(map[string]any); !ok {
				return errInvalid
			}
		case "items", "todos_checked", "todos_unchecked", "todos_added", "plan_steps_checked", "plan_steps_unchecked", "plans_approved", "plans_rejected":
			if _, ok := value.([]any); !ok {
				return errInvalid
			}
		default:
			if err := text(value); err != nil {
				return err
			}
		}
	}
	return nil
}
