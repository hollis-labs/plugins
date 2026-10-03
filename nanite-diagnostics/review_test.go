package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func TestLiteralReviewedManifest(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, raw, []byte(`[{"name":"readonly.query","reason":"Workspace-wide READ authority over recorded usage, execution metrics and captured slot accounting for any session in this workspace. No content and no mutations.","metadata":{"resources":["usage","execution_metrics","context_slots"],"all_sessions":true}}]`))
	if len(m.Tools) != 0 {
		t.Fatal("agent tools added")
	}
	var block pluginapi.Block
	if err := manifest.DecodeExtension(m.Nanite, &block); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(block.Registers.HTTPRoutes, []pluginapi.Route{{Method: "GET", Path: "diagnostics"}}) {
		t.Fatalf("routes expanded: %+v", block.Registers.HTTPRoutes)
	}
}

// Start every malformed case from an otherwise valid core-produced value.
// This prevents unrelated absent fields from masking the guard under test.
func TestRequiredAccountingFields(t *testing.T) {
	fixtures := goldens(t)
	for resource, name := range map[string]string{"usage": "usage", "execution_metrics": "metrics", "context_slots": "slots"} {
		var object map[string]any
		if err := json.Unmarshal(fixtures[name].Data, &object); err != nil {
			t.Fatal(err)
		}
		targets := []map[string]any{object}
		if resource == "execution_metrics" {
			targets = append(targets, object["metrics"].([]any)[0].(map[string]any))
		}
		if resource == "context_slots" {
			targets = append(targets, object["slots"].([]any)[0].(map[string]any))
		}
		for _, target := range targets {
			for field, value := range target {
				if field == "profile_name" || field == "profile_digest" || field == "cache_key" {
					continue
				}
				t.Run(resource+"/"+field, func(t *testing.T) {
					delete(target, field)
					raw, err := json.Marshal(object)
					if err != nil {
						t.Fatal(err)
					}
					target[field] = value
					assertBadResource(t, resource, raw)
				})
			}
		}
	}
}

func assertBadResource(t *testing.T, resource string, raw json.RawMessage) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostReply(w, r, raw) }))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	got := callHTTP(t, p, "session_id=session-a&resource="+resource)
	var decoded singleResponse
	if err := json.Unmarshal(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if got.Status != 200 || decoded.Result.Data != nil || decoded.Result.Code != "invalid_response" {
		t.Fatalf("invalid %s trusted: %s", resource, got.Body)
	}
}

func TestIsolatedAccountingGuards(t *testing.T) {
	fixtures := goldens(t)
	for _, name := range []string{"metrics-session", "metrics-cap", "metrics-null", "metrics-more", "slots-turn", "slots-time", "slots-content", "slots-null"} {
		t.Run(name, func(t *testing.T) {
			resource, fixture := "execution_metrics", "metrics"
			if strings.HasPrefix(name, "slots") {
				resource, fixture = "context_slots", "slots"
			}
			var obj map[string]any
			if err := json.Unmarshal(fixtures[fixture].Data, &obj); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "metrics-session":
				obj["metrics"].([]any)[0].(map[string]any)["session_id"] = "other"
			case "metrics-cap":
				obj["metrics"] = append(obj["metrics"].([]any), obj["metrics"].([]any)[0])
			case "metrics-null":
				obj["metrics"] = nil
			case "metrics-more":
				delete(obj, "more")
			case "slots-turn":
				delete(obj, "turn_id")
			case "slots-time":
				obj["started_at"] = "bad time"
			case "slots-content":
				obj["slots"].([]any)[0].(map[string]any)["content"] = "PRIVATE PROMPT"
			case "slots-null":
				obj["slots"] = nil
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			assertBadResource(t, resource, raw)
		})
	}
}

func TestAdditiveAccountingFields(t *testing.T) {
	fixtures := goldens(t)
	for resource, name := range map[string]string{"usage": "usage", "execution_metrics": "metrics", "context_slots": "slots"} {
		t.Run(resource, func(t *testing.T) {
			var obj map[string]any
			if err := json.Unmarshal(fixtures[name].Data, &obj); err != nil {
				t.Fatal(err)
			}
			obj["reasoning_tokens"], obj["partial_rows"] = 17, 2
			if resource == "execution_metrics" {
				obj["metrics"].([]any)[0].(map[string]any)["future_field"] = "extra"
			}
			if resource == "context_slots" {
				obj["slots"].([]any)[0].(map[string]any)["future_field"] = "extra"
			}
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostReply(w, r, raw) }))
			defer server.Close()
			p := &diagnosticsPlugin{}
			initialize(t, p, grantFor(server.URL))
			got := callHTTP(t, p, "session_id=session-a&resource="+resource)
			var decoded struct {
				Result struct {
					Data json.RawMessage `json:"data"`
				} `json:"result"`
			}
			if err := json.Unmarshal(got.Body, &decoded); err != nil {
				t.Fatal(err)
			}
			equalJSON(t, decoded.Result.Data, fixtures[name].Data)
		})
	}
}

func TestTimeOrderingGolden(t *testing.T) {
	fixture := goldens(t)["metrics-time-order"].Data
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostReply(w, r, fixture) }))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	got := callHTTP(t, p, "session_id=session-a&resource=execution_metrics")
	var decoded struct {
		Result struct {
			Data pluginapi.QueryMetricsData `json:"data"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if rows := decoded.Result.Data.Metrics; len(rows) != 50 || rows[0].ID != 2 || rows[1].ID != 3 || rows[2].ID != 51 {
		t.Fatalf("core time order changed: %s", got.Body)
	}
}

type queryFunc func(context.Context, pluginapi.QueryRequest) (pluginapi.QueryResponse, error)

func (f queryFunc) Query(ctx context.Context, q pluginapi.QueryRequest) (pluginapi.QueryResponse, error) {
	return f(ctx, q)
}

func TestEachReadHasIndependentDeadline(t *testing.T) {
	fixtures := goldens(t)
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor("http://127.0.0.1:1234"))
	// Observe the context at the query boundary, before QueryClient adds its own
	// HTTP-client timeout. Otherwise the SDK would mask a missing plugin deadline.
	p.client = queryFunc(func(ctx context.Context, q pluginapi.QueryRequest) (pluginapi.QueryResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 25*time.Second || time.Until(deadline) < 24*time.Second {
			t.Error("missing 25-second resource deadline")
		}
		if q.Resource == pluginapi.QueryUsage {
			return pluginapi.QueryResponse{}, context.DeadlineExceeded
		}
		name := map[pluginapi.QueryResource]string{pluginapi.QueryExecutionMetrics: "metrics", pluginapi.QueryContextSlots: "slots"}[q.Resource]
		return pluginapi.QueryResponse{Protocol: 1, Resource: q.Resource, SessionID: q.SessionID, Data: fixtures[name].Data}, nil
	})
	got := callHTTP(t, p, "session_id=session-a")
	var decoded diagnosticsResponse
	if err := json.Unmarshal(got.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Usage.Code != "canceled" || decoded.Metrics.Data == nil || decoded.Slots.Data == nil {
		t.Fatalf("timeout discarded healthy resources: %s", got.Body)
	}
}

func TestStalledResourceDoesNotBlockOtherRequests(t *testing.T) {
	fixtures := goldens(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/usage") {
			close(entered)
			<-r.Context().Done()
			return
		}
		name := map[string]string{"execution_metrics": "metrics", "context_slots": "slots"}[strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/")]
		hostReply(w, r, fixtures[name].Data)
	}))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan subprocess.HTTPResponse, 1)
	go func() {
		got, err := p.HTTPHandle(ctx, subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=session-a&resource=usage"})
		if err != nil {
			t.Error(err)
		}
		finished <- got
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stalled read never entered")
	}
	for _, resource := range []string{"execution_metrics", "context_slots"} {
		got := callHTTP(t, p, "session_id=session-a&resource="+resource)
		if got.Status != 200 || !strings.Contains(string(got.Body), `"data":`) {
			t.Fatalf("healthy resource blocked: %s", got.Body)
		}
	}
	cancel()
	select {
	case got := <-finished:
		if !strings.Contains(string(got.Body), `"code":"canceled"`) {
			t.Fatalf("cancel missing resource error: %s", got.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled read did not cancel")
	}
}

func TestOversizedEscapedWrapper(t *testing.T) {
	var obj map[string]any
	if err := json.Unmarshal(goldens(t)["metrics"].Data, &obj); err != nil {
		t.Fatal(err)
	}
	rows := obj["metrics"].([]any)
	obj["metrics"] = rows[:1]
	rows[0].(map[string]any)["provider"] = strings.Repeat("<", 700000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The host can send unescaped '<' inside its 1 MiB reply; re-encoding expands it sixfold.
		data, err := json.Marshal(obj)
		if err != nil {
			t.Error(err)
		}
		raw := strings.ReplaceAll(string(data), `\u003c`, "<")
		envelope := `{"protocol":1,"resource":"execution_metrics","session_id":"session-a","data":` + raw + `}`
		if len(envelope) > pluginapi.MaxQueryResponseBytes {
			t.Error("test host exceeded host bound")
		}
		_, _ = w.Write([]byte(envelope))
	}))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	got := callHTTP(t, p, "session_id=session-a&resource=execution_metrics")
	if got.Status != 413 {
		t.Fatalf("oversize wrapper not rejected at wrapper bound: %d %s", got.Status, string(got.Body[:min(len(got.Body), 200)]))
	}
}

// Pin the accounting-only transport: a future struct field must not make
// captured prompt text serializable, even if current guards reject the host key.
func TestSlotTransportCannotSerializeContent(t *testing.T) {
	value := reflect.New(reflect.TypeFor[accountingSlot]()).Elem()
	for i := 0; i < value.NumField(); i++ {
		if value.Field(i).Kind() == reflect.String {
			value.Field(i).SetString("fixture")
		}
	}
	raw, err := json.Marshal(value.Interface())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["content"]; present {
		t.Fatal("captured content became serializable")
	}
}

func TestSingleResourceLiteralWireContract(t *testing.T) {
	usage := goldens(t)["usage"].Data
	for _, denied := range []bool{false, true} {
		name := "success"
		if denied {
			name = "denied"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if denied {
					http.Error(w, "private detail", http.StatusForbidden)
					return
				}
				hostReply(w, r, usage)
			}))
			defer server.Close()
			p := &diagnosticsPlugin{}
			initialize(t, p, grantFor(server.URL))
			got := callHTTP(t, p, "session_id=session-a&resource=usage")
			if got.Status != 200 {
				t.Fatalf("wire status: %d", got.Status)
			}
			// Literal UI-facing keys/coordinates; no plugin response structs or tags.
			expected := []byte(`{"session_id":"session-a","resource":"usage","result":{"data":` + string(usage) + `}}`)
			if denied {
				expected = []byte(`{"session_id":"session-a","resource":"usage","result":{"error":"Session or resource is outside the approved read scope","code":"host_403"}}`)
			}
			equalJSON(t, got.Body, expected)
			if !denied {
				if path := os.Getenv("DIAGNOSTICS_WIRE_CAPTURE_OUT"); path != "" {
					if err := os.WriteFile(path, append(got.Body, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestSlotContentKeysCaseInsensitive(t *testing.T) {
	for _, key := range []string{"content", "Content", "CONTENT"} {
		t.Run(key, func(t *testing.T) {
			var obj map[string]any
			if err := json.Unmarshal(goldens(t)["slots"].Data, &obj); err != nil {
				t.Fatal(err)
			}
			obj["slots"].([]any)[0].(map[string]any)[key] = "PRIVATE PROMPT"
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			assertBadResource(t, "context_slots", raw)
		})
	}
}
