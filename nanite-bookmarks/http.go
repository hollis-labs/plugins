package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"io"
	"net/http"
)

func (p *bookmarks) HTTPHandle(ctx context.Context, req subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /bookmarks", p.listHTTP)
	mux.HandleFunc("POST /bookmarks", p.saveHTTP(false))
	mux.HandleFunc("POST /toggle", p.saveHTTP(true))
	mux.HandleFunc("POST /latest", p.latestHTTP)
	mux.HandleFunc("DELETE /bookmarks/{id}", p.changeHTTP(false))
	mux.HandleFunc("PATCH /bookmarks/{id}", p.changeHTTP(true))
	return pluginapi.HandleHTTP(ctx, pluginID, mux, req)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "Cannot encode response", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
func failure(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	if errors.Is(err, errNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func (p *bookmarks) listHTTP(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session_id")
	if err := (pluginapi.CoreReference{SessionID: session, MessageID: "validation"}).Validate(); err != nil {
		writeJSON(w, 400, map[string]string{"error": "session_id required"})
		return
	}
	source, err := p.ready(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	rows, err := p.db.list(r.Context(), source, session)
	if err != nil {
		failure(w, err)
		return
	}
	offset, err := queryOffset(r.URL.Query().Get("offset"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid offset"})
		return
	}
	page, err := pageRows(rows, offset)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid offset"})
		return
	}
	writeJSON(w, 200, page)
}
func decodeBody(r *http.Request, out any) error {
	bounded := &io.LimitedReader{R: r.Body, N: (16 << 10) + 1}
	decoder := json.NewDecoder(bounded)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || bounded.N <= 0 {
		return fmt.Errorf("invalid or oversized body")
	}
	return nil
}
func (p *bookmarks) saveHTTP(toggle bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SessionID string `json:"session_id"`
			MessageID string `json:"message_id"`
			Note      string `json:"note"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid bookmark"})
			return
		}
		reference := pluginapi.CoreReference{SessionID: body.SessionID, MessageID: body.MessageID}
		if err := reference.Validate(); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid reference"})
			return
		}
		if err := validateNote(body.Note); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid note"})
			return
		}
		row, removed, err := p.save(r.Context(), reference, body.Note, toggle)
		if err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"bookmark": row, "removed": removed})
	}
}
func (p *bookmarks) changeHTTP(edit bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var note *string
		if edit {
			var body struct {
				Note string `json:"note"`
			}
			if err := decodeBody(r, &body); err != nil {
				writeJSON(w, 400, map[string]string{"error": "invalid note"})
				return
			}
			if err := validateNote(body.Note); err != nil {
				writeJSON(w, 400, map[string]string{"error": "invalid note"})
				return
			}
			note = &body.Note
		}
		source, err := p.ready(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		if err = p.db.change(r.Context(), source, r.PathValue("id"), note); err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

func (p *bookmarks) latestHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Note      string `json:"note"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid bookmark"})
		return
	}
	reference, err := p.latest(r.Context(), body.SessionID)
	if err != nil {
		failure(w, err)
		return
	}
	row, _, err := p.save(r.Context(), reference, body.Note, false)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, 200, row)
}
