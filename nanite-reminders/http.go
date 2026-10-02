package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func (p *remindersPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	if call.Path == pluginapi.ContextFetchPath {
		return p.contextFetch(ctx, call)
	}
	mux := http.NewServeMux()
	send := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(value)
	}
	decode := func(w http.ResponseWriter, r *http.Request, value any) bool {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil {
			http.Error(w, "request exceeds limit", http.StatusRequestEntityTooLarge)
			return false
		}
		if err = manifest.DecodeExtension(raw, value); err != nil {
			http.Error(w, "invalid reminder request", http.StatusBadRequest)
			return false
		}
		return true
	}
	session := func(w http.ResponseWriter, r *http.Request) (string, pluginapi.QuerySession, bool) {
		id := r.URL.Query().Get("session_id")
		if call.SessionID != "" && call.SessionID != id {
			http.Error(w, "session differs from caller", http.StatusForbidden)
			return "", pluginapi.QuerySession{}, false
		}
		source, err := p.ready(r.Context())
		if err != nil {
			http.Error(w, "reminders unavailable", http.StatusServiceUnavailable)
			return "", pluginapi.QuerySession{}, false
		}
		meta, _, err := p.session(r.Context(), id)
		if err != nil {
			http.Error(w, "current session unavailable", http.StatusBadRequest)
			return "", pluginapi.QuerySession{}, false
		}
		return source, meta, true
	}
	mux.HandleFunc("GET /reminders", func(w http.ResponseWriter, r *http.Request) {
		source, meta, ok := session(w, r)
		if !ok {
			return
		}
		offset := 0
		if r.URL.Query().Has("offset") {
			var err error
			offset, err = strconv.Atoi(r.URL.Query().Get("offset"))
			if err != nil || offset < 0 || offset > pluginapi.MaxDataExportRows {
				http.Error(w, "invalid offset", http.StatusBadRequest)
				return
			}
		}
		rows, err := p.db.list(r.Context(), source, meta.ID, meta.ProjectID)
		if err != nil {
			http.Error(w, "could not list reminders", http.StatusInternalServerError)
			return
		}
		start := min(offset, len(rows))
		end := min(start+100, len(rows))
		send(w, map[string]any{"reminders": rows[start:end], "more": end < len(rows), "next_offset": end, "project_id": meta.ProjectID})
	})
	mux.HandleFunc("POST /reminders", func(w http.ResponseWriter, r *http.Request) {
		_, meta, ok := session(w, r)
		if !ok {
			return
		}
		var req createRequest
		if !decode(w, r, &req) {
			return
		}
		row, err := p.create(r.Context(), meta.ID, req)
		if err != nil {
			http.Error(w, "invalid or unavailable reminder", http.StatusBadRequest)
			return
		}
		send(w, row)
	})
	mutation := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			source, meta, ok := session(w, r)
			if !ok {
				return
			}
			scope := ""
			if action == "scope" {
				var req struct {
					Scope string `json:"scope"`
				}
				if !decode(w, r, &req) {
					return
				}
				scope = req.Scope
			}
			err := p.db.change(r.Context(), source, meta.ID, meta.ProjectID, r.PathValue("id"), action, scope)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, errNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, "reminder mutation failed", status)
				return
			}
			send(w, map[string]string{"id": r.PathValue("id"), "status": action})
		}
	}
	mux.HandleFunc("POST /reminders/{id}/ack", mutation("ack"))
	mux.HandleFunc("PATCH /reminders/{id}/scope", mutation("scope"))
	mux.HandleFunc("DELETE /reminders/{id}", mutation("delete"))
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}
func (p *remindersPlugin) contextFetch(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	request, err := pluginapi.DecodeContextRequest(&call)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusBadRequest}, nil
	}
	if request.SourceID != "reminders" {
		return subprocess.HTTPResponse{Status: http.StatusNotFound}, nil
	}
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	meta, count, err := p.session(ctx, request.SessionID)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	rows, err := p.db.due(ctx, source, request.SessionID, meta.ProjectID, count, time.Now())
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	response := pluginapi.ContextResponse{Protocol: pluginapi.ContextProtocol, Items: []pluginapi.ContextItem{}}
	remaining := request.TokenBudget
	for _, row := range rows {
		if !utf8.ValidString(row.Text) || !utf8.ValidString(row.ID) {
			continue
		}
		content := fmt.Sprintf("<system-reminder>\nReminder %s: %s\nAcknowledge with reminders_ack after handling it.\n</system-reminder>", row.ID, row.Text)
		// Match the host's conservative byte estimate; retrieval never consumes a
		// reminder and a future larger budget can still deliver an omitted item.
		cost := len(content) + len(row.ID) + len("plugin/"+pluginID+"/reminders") + 64
		if cost > remaining {
			continue
		}
		candidate := pluginapi.ContextItem{Key: row.ID, Content: content, Relevance: 1}
		response.Items = append(response.Items, candidate)
		trial, marshalErr := json.Marshal(response)
		if marshalErr != nil {
			return subprocess.HTTPResponse{}, marshalErr
		}
		if len(trial) > pluginapi.MaxContextBytes {
			response.Items = response.Items[:len(response.Items)-1]
			continue
		}
		remaining -= cost
		if len(response.Items) == pluginapi.MaxContextItems {
			break
		}
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return subprocess.HTTPResponse{}, err
	}
	if len(raw) > pluginapi.MaxContextBytes {
		return subprocess.HTTPResponse{Status: http.StatusRequestEntityTooLarge}, nil
	}
	return subprocess.HTTPResponse{Status: http.StatusOK, Body: raw, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
