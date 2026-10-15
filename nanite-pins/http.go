package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func (p *pinsPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	if call.Path == pluginapi.AlwaysShipFetchPath {
		return p.alwaysShipFetch(ctx, call)
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
			http.Error(w, "invalid pin request", http.StatusBadRequest)
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
			http.Error(w, "pins unavailable", http.StatusServiceUnavailable)
			return "", pluginapi.QuerySession{}, false
		}
		meta, err := p.session(r.Context(), id)
		if err != nil {
			http.Error(w, "current session unavailable", http.StatusBadRequest)
			return "", pluginapi.QuerySession{}, false
		}
		return source, meta, true
	}
	mux.HandleFunc("GET /pins", func(w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, "could not list pins", http.StatusInternalServerError)
			return
		}
		start := min(offset, len(rows))
		end := min(start+100, len(rows))
		send(w, map[string]any{"pins": rows[start:end], "more": end < len(rows), "next_offset": end, "project_id": meta.ProjectID})
	})
	mux.HandleFunc("POST /pins", func(w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, "invalid or unavailable pin", http.StatusBadRequest)
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
			value := ""
			if action == "content" {
				var req struct {
					Content string `json:"content"`
				}
				if !decode(w, r, &req) {
					return
				}
				value = req.Content
			}
			if action == "scope" {
				var req struct {
					Scope string `json:"scope"`
				}
				if !decode(w, r, &req) {
					return
				}
				value = req.Scope
			}
			err := p.db.change(r.Context(), source, meta.ID, meta.ProjectID, r.PathValue("id"), action, value)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, errNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, "pin mutation failed", status)
				return
			}
			send(w, map[string]string{"id": r.PathValue("id"), "status": action})
		}
	}
	mux.HandleFunc("PATCH /pins/{id}", mutation("content"))
	mux.HandleFunc("PATCH /pins/{id}/scope", mutation("scope"))
	mux.HandleFunc("DELETE /pins/{id}", mutation("delete"))
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}
