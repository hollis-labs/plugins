package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Content is intentionally not part of this transport, even if a host misbehaves.
type accountingSlot struct {
	Name         string `json:"name"`
	Tokens       int    `json:"tokens"`
	Cached       bool   `json:"cached"`
	CacheKey     string `json:"cache_key,omitempty"`
	Sensitive    bool   `json:"sensitive"`
	TrafficLight string `json:"traffic_light"`
}
type accountingSlots struct {
	Available bool             `json:"available"`
	TurnID    string           `json:"turn_id,omitempty"`
	StartedAt string           `json:"started_at,omitempty"`
	Slots     []accountingSlot `json:"slots"`
}
type resourceResult struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}
type diagnosticsResponse struct {
	SessionID string         `json:"session_id"`
	Usage     resourceResult `json:"usage"`
	Metrics   resourceResult `json:"execution_metrics"`
	Slots     resourceResult `json:"context_slots"`
}

func validSession(id string) bool {
	return (pluginapi.QueryScope{Resources: resources, AllSessions: true}).Allows(pluginapi.QueryUsage, id) && id != ""
}

func (p *diagnosticsPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /diagnostics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Only GET is supported", http.StatusMethodNotAllowed)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q) != 1 || len(q["session_id"]) != 1 || !validSession(q.Get("session_id")) || (call.SessionID != "" && call.SessionID != q.Get("session_id")) || len(call.Body) != 0 {
			http.Error(w, "One valid calling session_id is required; other parameters and request bodies are not supported", http.StatusBadRequest)
			return
		}
		p.mu.RLock()
		client, lifetime := p.client, p.lifetime
		p.mu.RUnlock()
		if client == nil || lifetime == nil || lifetime.Err() != nil {
			http.Error(w, "Diagnostics read grant unavailable", http.StatusServiceUnavailable)
			return
		}
		readCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		stop := context.AfterFunc(lifetime, cancel)
		defer cancel()
		defer stop()
		session := q.Get("session_id")
		result := diagnosticsResponse{SessionID: session}
		result.Usage = readResource(readCtx, client, pluginapi.QueryUsage, session)
		result.Metrics = readResource(readCtx, client, pluginapi.QueryExecutionMetrics, session)
		result.Slots = readResource(readCtx, client, pluginapi.QueryContextSlots, session)
		if lifetime.Err() != nil || readCtx.Err() != nil {
			http.Error(w, "Diagnostics read canceled or timed out", http.StatusGatewayTimeout)
			return
		}
		raw, err := json.Marshal(result)
		if err != nil {
			http.Error(w, "Diagnostics response unavailable", http.StatusInternalServerError)
			return
		}
		// Three independently bounded host replies plus the wrapper fit the 4 MiB SDK buffer.
		if len(raw) > 3*pluginapi.MaxQueryResponseBytes+4096 {
			http.Error(w, "Diagnostics response exceeds limit", http.StatusRequestEntityTooLarge)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}

func readResource(ctx context.Context, client *pluginapi.QueryClient, resource pluginapi.QueryResource, session string) resourceResult {
	query := pluginapi.QueryRequest{Resource: resource, SessionID: session}
	if resource == pluginapi.QueryExecutionMetrics {
		query.Limit = 50
	}
	reply, err := client.Query(ctx, query)
	if err != nil {
		return safeReadError(err)
	}
	var data any
	switch resource {
	case pluginapi.QueryUsage:
		data = &pluginapi.QueryUsageData{}
	case pluginapi.QueryExecutionMetrics:
		data = &pluginapi.QueryMetricsData{}
	case pluginapi.QueryContextSlots:
		data = &accountingSlots{}
	default:
		return resourceResult{Code: "invalid", Error: "Unsupported diagnostics resource"}
	}
	if err := manifest.DecodeExtension(reply.Data, data); err != nil {
		return resourceResult{Code: "invalid_response", Error: "Host returned invalid accounting data"}
	}
	if !validData(reply.Data, data, session) {
		return resourceResult{Code: "invalid_response", Error: "Host returned incomplete or mismatched accounting data"}
	}
	return resourceResult{Data: data}
}

func validData(raw json.RawMessage, data any, session string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	var required []string
	switch value := data.(type) {
	case *pluginapi.QueryUsageData:
		required = []string{"input_tokens", "output_tokens", "total_tokens", "tool_input_tokens", "cache_creation_tokens", "cache_read_tokens", "estimated_cost_usd", "message_count"}
	case *pluginapi.QueryMetricsData:
		required = []string{"metrics", "more"}
		if value.Metrics == nil || len(value.Metrics) > 50 {
			return false
		}
		for _, row := range value.Metrics {
			if row.SessionID != session {
				return false
			}
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(fields["metrics"], &rows) != nil {
			return false
		}
		for _, row := range rows {
			if !hasFields(row, "id", "session_id", "message_id", "provider", "adapter", "model", "agent_id", "agent_slug", "mode", "duration_ms", "context_messages", "context_tokens", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "estimated_cost_usd", "tool_iterations", "tool_calls", "is_utility", "stop_reason", "failed", "created_at") {
				return false
			}
		}
	case *accountingSlots:
		required = []string{"available", "slots"}
		if value.Slots == nil || (!value.Available && (len(value.Slots) != 0 || value.TurnID != "" || value.StartedAt != "")) {
			return false
		}
		if value.Available {
			if value.TurnID == "" {
				return false
			}
			if _, err := time.Parse(time.RFC3339Nano, value.StartedAt); err != nil {
				return false
			}
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(fields["slots"], &rows) != nil {
			return false
		}
		for _, row := range rows {
			if !hasFields(row, "name", "tokens", "cached", "sensitive", "traffic_light") {
				return false
			}
		}
	default:
		return false
	}
	return hasFields(fields, required...)
}

func hasFields(fields map[string]json.RawMessage, required ...string) bool {
	for _, key := range required {
		if len(fields[key]) == 0 || string(fields[key]) == "null" {
			return false
		}
	}
	return true
}

// QueryClient exposes no typed HTTP error. Match only its fixed public status
// message; transport/decoder details (which can contain credentials) never leave here.
func safeReadError(err error) resourceResult {
	for status, message := range map[int]string{
		401: "Host read grant was revoked or is invalid",
		403: "Session or resource is outside the approved read scope",
		404: "Session was not found",
		413: "Recorded accounting exceeds the host response limit",
		500: "Host could not read recorded accounting",
		503: "Host accounting queries are unavailable",
		504: "Host accounting read timed out or was canceled",
	} {
		if err.Error() == fmt.Sprintf("pluginapi: query host returned HTTP %d", status) {
			return resourceResult{Code: fmt.Sprintf("host_%d", status), Error: message}
		}
	}
	return resourceResult{Code: "unavailable", Error: "Accounting read unavailable: check the approved scope and host connection"}
}
