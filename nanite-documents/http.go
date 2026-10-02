package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func (p *documentsPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	if call.Path == pluginapi.ContextFetchPath {
		return p.contextFetch(ctx, call)
	}
	mux := http.NewServeMux()
	send := func(w http.ResponseWriter, v any) {
		raw, err := json.Marshal(v)
		if err != nil || len(raw) > maxResponseBytes {
			http.Error(w, "response exceeds limit", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(raw)
	}
	decode := func(w http.ResponseWriter, r *http.Request, v any) bool {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginapi.MaxHTTPBody))
		if err != nil {
			http.Error(w, "request exceeds limit", http.StatusRequestEntityTooLarge)
			return false
		}
		if err = decodeRequest(raw, v); err != nil {
			http.Error(w, "invalid document request", http.StatusBadRequest)
			return false
		}
		return true
	}
	session := func(w http.ResponseWriter, r *http.Request) (string, string, bool) {
		values := r.URL.Query()["session_id"]
		if len(values) != 1 || values[0] == "" {
			http.Error(w, "one session_id required", http.StatusBadRequest)
			return "", "", false
		}
		id := values[0]
		if call.SessionID != "" && call.SessionID != id {
			http.Error(w, "session differs from caller", http.StatusForbidden)
			return "", "", false
		}
		source, err := p.ready(r.Context())
		if err != nil {
			http.Error(w, "documents unavailable", http.StatusServiceUnavailable)
			return "", "", false
		}
		if _, err = p.session(r.Context(), id); err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, errNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, "current session unavailable", status)
			return "", "", false
		}
		return source, id, true
	}
	failed := func(w http.ResponseWriter, err error) {
		status := http.StatusInternalServerError
		if errors.Is(err, errInvalid) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, errQuota) {
			status = http.StatusRequestEntityTooLarge
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		if errors.Is(err, context.Canceled) {
			status = http.StatusServiceUnavailable
		}
		if errors.Is(err, errNotFound) {
			status = http.StatusNotFound
		}
		message := "document storage unavailable"
		if errors.Is(err, errInvalid) {
			message = "invalid document request"
		}
		if errors.Is(err, errNotFound) {
			message = "document not found"
		}
		if errors.Is(err, errQuota) {
			message = err.Error()
		}
		if errors.Is(err, errCommitUncertain) {
			message = errCommitUncertain.Error()
		}
		http.Error(w, message, status)
	}
	integer := func(r *http.Request, k string) (int, error) {
		if !r.URL.Query().Has(k) {
			return 0, nil
		}
		value, err := strconv.Atoi(r.URL.Query().Get(k))
		if err != nil {
			return 0, fmt.Errorf("%w: invalid %s", errInvalid, k)
		}
		return value, nil
	}
	mux.HandleFunc("GET /documents", func(w http.ResponseWriter, r *http.Request) {
		source, id, ok := session(w, r)
		if !ok {
			return
		}
		offset, err := integer(r, "offset")
		if err != nil {
			failed(w, err)
			return
		}
		v, err := p.db.page(r.Context(), source, id, offset)
		if err != nil {
			failed(w, err)
			return
		}
		send(w, v)
	})
	mux.HandleFunc("GET /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		source, id, ok := session(w, r)
		if !ok {
			return
		}
		offset, err := integer(r, "offset")
		if err != nil {
			failed(w, err)
			return
		}
		limit, err := integer(r, "limit")
		if err != nil {
			failed(w, err)
			return
		}
		v, err := p.db.read(r.Context(), source, id, r.PathValue("id"), offset, limit)
		if err != nil {
			failed(w, err)
			return
		}
		send(w, v)
	})
	mux.HandleFunc("POST /documents", func(w http.ResponseWriter, r *http.Request) {
		_, id, ok := session(w, r)
		if !ok {
			return
		}
		var req createRequest
		if !decode(w, r, &req) {
			return
		}
		row, err := p.create(r.Context(), id, req)
		if err != nil {
			failed(w, err)
			return
		}
		row.Content = ""
		send(w, row)
	})
	mux.HandleFunc("PATCH /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		source, id, ok := session(w, r)
		if !ok {
			return
		}
		var req patchRequest
		if !decode(w, r, &req) {
			return
		}
		if err := p.db.change(r.Context(), source, id, r.PathValue("id"), req, false); err != nil {
			failed(w, err)
			return
		}
		send(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("DELETE /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		source, id, ok := session(w, r)
		if !ok {
			return
		}
		if err := p.db.change(r.Context(), source, id, r.PathValue("id"), patchRequest{}, true); err != nil {
			failed(w, err)
			return
		}
		send(w, map[string]bool{"ok": true})
	})
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}
func (p *documentsPlugin) contextFetch(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	req, err := pluginapi.DecodeContextRequest(&call)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusBadRequest}, nil
	}
	if req.SourceID != "documents" {
		return subprocess.HTTPResponse{Status: http.StatusNotFound}, nil
	}
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	if _, err = p.session(ctx, req.SessionID); err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	rows, err := p.db.list(ctx, source, req.SessionID)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	response := buildDocumentContext(rows, req.TokenBudget)
	raw, err := json.Marshal(response)
	return subprocess.HTTPResponse{Status: http.StatusOK, Body: raw, Headers: map[string]string{"Content-Type": "application/json"}}, err
}

// These formats are the per-document strings in core buildUserContextSlot.
func documentContext(row document, full bool) string {
	if full {
		return fmt.Sprintf("### Document: %s\n%s", row.Name, row.Content)
	}
	summary := row.Summary
	if summary == "" {
		summary = fmt.Sprintf("(document ID: %s, size: %d bytes)", row.ID, row.SizeBytes)
	}
	return fmt.Sprintf("### Document: %s (pointer)\n%s", row.Name, summary)
}
func buildDocumentContext(rows []document, budget int) pluginapi.ContextResponse {
	response := pluginapi.ContextResponse{Protocol: pluginapi.ContextProtocol, Items: []pluginapi.ContextItem{}}
	remaining := budget
	for index, row := range rows {
		if !row.Included || row.ID == "" || !utf8.ValidString(row.ID) || len(row.ID) > 256 {
			continue
		}
		// If full text cannot fit, try the pointer in its place. No truncation or
		// inclusion state mutation; if the pointer also cannot fit, skip it.
		forms := []bool{false}
		if row.FullContent {
			forms = []bool{true, false}
		}
		for _, full := range forms {
			content := documentContext(row, full)
			if !utf8.ValidString(content) {
				continue
			}
			cost := len(content) + len(row.ID) + len("plugin/"+pluginID+"/documents") + 64
			if cost > remaining {
				continue
			}
			item := pluginapi.ContextItem{Key: row.ID, Content: content, Relevance: 1 - float64(index)/float64(len(rows)+1)}
			response.Items = append(response.Items, item)
			raw, err := json.Marshal(response)
			if err != nil || len(raw) > pluginapi.MaxContextBytes {
				response.Items = response.Items[:len(response.Items)-1]
				continue
			}
			remaining -= cost
			break
		}
		if len(response.Items) == pluginapi.MaxContextItems {
			break
		}
	}
	return response
}
