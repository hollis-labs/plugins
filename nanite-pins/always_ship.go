package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func omissionLine(count int) string {
	return fmt.Sprintf("%d more pins not included; use pins_list, then pins_get (requires an agent tool grant; if unavailable, open the Pins plugin UI).", count)
}

func pinLabel(row pin) string {
	if row.Scope == "project" {
		return "[pinned:project] "
	}
	return "[pinned] "
}

// renderPins preserves the pre-cutover body when it fits. Oldest-first
// demotion is NEW: drop whole rows from the start, keeping the surviving
// suffix in the store's stable chronological order. Reads never mutate pins.
func renderPins(rows []pin, allowance int) (string, error) {
	eligible := make([]pin, 0, len(rows))
	size := 0
	for _, row := range rows {
		if row.Scope == "turn" {
			continue
		}
		if !utf8.ValidString(row.Content) || strings.ContainsRune(row.Content, 0) {
			return "", fmt.Errorf("pin content cannot be rendered")
		}
		if len(eligible) > 0 {
			size++ // historic one-newline separator
		}
		size += len(pinLabel(row)) + len(row.Content)
		eligible = append(eligible, row)
	}
	if len(eligible) == 0 {
		return "", nil
	}
	dropped := 0
	marker := ""
	if size > allowance {
		for dropped < len(eligible) {
			size -= len(pinLabel(eligible[dropped])) + len(eligible[dropped].Content)
			dropped++
			if dropped < len(eligible) {
				size--
			}
			marker = omissionLine(dropped)
			cost := size + len(marker)
			if dropped < len(eligible) {
				cost++ // separator before marker
			}
			if cost <= allowance {
				break
			}
		}
		if size+len(marker) > allowance {
			return "", fmt.Errorf("pin omission notice exceeds allowance")
		}
	}
	parts := make([]string, 0, len(eligible)-dropped+1)
	for _, row := range eligible[dropped:] {
		parts = append(parts, pinLabel(row)+row.Content)
	}
	if marker != "" {
		parts = append(parts, marker)
	}
	return strings.Join(parts, "\n"), nil
}

func (p *pinsPlugin) alwaysShipFetch(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	request, err := pluginapi.DecodeAlwaysShipRequest(&call)
	if err != nil || call.RawQuery != "" {
		return subprocess.HTTPResponse{Status: http.StatusBadRequest}, nil
	}
	if request.SourceID != "pins" {
		return subprocess.HTTPResponse{Status: http.StatusNotFound}, nil
	}
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	meta, err := p.session(ctx, request.SessionID)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	rows, err := p.db.list(ctx, source, request.SessionID, meta.ProjectID)
	if err != nil {
		return subprocess.HTTPResponse{Status: http.StatusServiceUnavailable}, nil
	}
	body, err := renderPins(rows, request.MaxBytes)
	if err != nil {
		// Never empty-success a nonempty inventory: host keeps its fallback.
		return subprocess.HTTPResponse{Status: http.StatusUnprocessableEntity}, nil
	}
	raw, err := json.Marshal(pluginapi.AlwaysShipResponse{Protocol: pluginapi.AlwaysShipProtocol, Body: body})
	if err != nil {
		return subprocess.HTTPResponse{}, err
	}
	if _, err = pluginapi.DecodeAlwaysShipResponse(raw, request.MaxBytes); err != nil {
		return subprocess.HTTPResponse{Status: http.StatusRequestEntityTooLarge}, nil
	}
	return subprocess.HTTPResponse{Status: http.StatusOK, Body: raw, Headers: map[string]string{"Content-Type": "application/json"}}, nil
}
