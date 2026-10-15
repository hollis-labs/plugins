package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type goldenSample struct {
	Name     string                  `json:"name"`
	Resource pluginapi.QueryResource `json:"resource"`
	Session  string                  `json:"session_id"`
	Limit    int                     `json:"limit"`
	Data     json.RawMessage         `json:"data"`
	Error    string                  `json:"error"`
}

func goldens(t *testing.T) map[string]goldenSample {
	t.Helper()
	raw, err := os.ReadFile("testdata/core-queries.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []goldenSample
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]goldenSample{}
	for _, row := range rows {
		out[row.Name] = row
	}
	return out
}
func grantFor(origin string) pluginapi.QueryGrant {
	return pluginapi.QueryGrant{Protocol: 1, PluginID: pluginID, HostURL: origin, Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Scope: pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QueryUsage, pluginapi.QueryExecutionMetrics, pluginapi.QueryContextSlots}, AllSessions: true}}
}
func identity(t *testing.T, grant pluginapi.QueryGrant) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"nanite_host_query": grant})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func initialize(t *testing.T, p *diagnosticsPlugin, grant pluginapi.QueryGrant) {
	t.Helper()
	_, err := p.Init(t.Context(), subprocess.InitParams{Identity: identity(t, grant)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Unload(context.Background()); err != nil {
			t.Error(err)
		}
	})
}
func callHTTP(t *testing.T, p *diagnosticsPlugin, query string) subprocess.HTTPResponse {
	t.Helper()
	response, err := p.HTTPHandle(t.Context(), subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: query})
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func hostReply(w http.ResponseWriter, r *http.Request, data json.RawMessage) {
	raw, _ := json.Marshal(pluginapi.QueryResponse{Protocol: 1, Resource: pluginapi.QueryResource(strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/")), SessionID: r.URL.Query().Get("session_id"), Data: data})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
func equalJSON(t *testing.T, a, b []byte) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("JSON changed:\n%s\nexpected:\n%s", a, b)
	}
}

func TestPinnedCoreProjectionParity(t *testing.T) {
	fixtures := goldens(t)
	for _, slotCase := range []string{"slots", "missing-capture", "empty-capture", "disabled-inspector"} {
		t.Run(slotCase, func(t *testing.T) {
			mapping := map[string]string{"usage": "usage", "execution_metrics": "metrics", "context_slots": slotCase}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				resource := strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/")
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+grantFor("").Token || r.URL.Query().Get("session_id") != "session-a" {
					t.Error("unexpected host read")
				}
				if resource == "execution_metrics" && r.URL.Query().Get("limit") != "50" {
					t.Error("metrics not bounded to 50")
				}
				if resource != "execution_metrics" && r.URL.Query().Has("limit") {
					t.Error("irrelevant limit")
				}
				hostReply(w, r, fixtures[mapping[resource]].Data)
			}))
			defer server.Close()
			p := &diagnosticsPlugin{}
			initialize(t, p, grantFor(server.URL))
			response := callHTTP(t, p, "session_id=session-a")
			if response.Status != 200 || response.Headers["Cache-Control"] != "no-store" {
				t.Fatalf("response %+v", response)
			}
			var decoded struct {
				Session string `json:"session_id"`
				Usage   struct{ Data json.RawMessage }
				Metrics struct{ Data json.RawMessage } `json:"execution_metrics"`
				Slots   struct{ Data json.RawMessage } `json:"context_slots"`
			}
			if err := json.Unmarshal(response.Body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Session != "session-a" {
				t.Fatal("session coordinate changed")
			}
			equalJSON(t, decoded.Usage.Data, fixtures["usage"].Data)
			equalJSON(t, decoded.Metrics.Data, fixtures["metrics"].Data)
			equalJSON(t, decoded.Slots.Data, fixtures[slotCase].Data)
			for _, secret := range []string{"private", "content", "debug_snapshots", "effective_limits_json", grantFor("").Token} {
				if strings.Contains(string(response.Body), secret) {
					t.Fatalf("private value/key escaped: %s", secret)
				}
			}
			if calls.Load() != 3 {
				t.Fatalf("unexpected host operations: %d", calls.Load())
			}
		})
	}
}

func TestHTTPValidationStopsBeforeHostRead(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	for _, query := range []string{"", "session_id=", "session_id=a&session_id=b", "session_id=../a", "session_id=a&resource=sessions", "session_id=a&resource=message_refs", "session_id=a&resource=unknown", "session_id=a&resource=", "session_id=a&resource=usage&resource=usage", "session_id=a&limit=500", "session_id=" + strings.Repeat("a", 129)} {
		if got := callHTTP(t, p, query); got.Status != 400 {
			t.Fatalf("%q -> %d", query, got.Status)
		}
	}
	if _, err := p.HTTPHandle(t.Context(), subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=%zz"}); err == nil {
		t.Fatal("malformed raw query accepted by SDK adapter")
	}
	for _, call := range []subprocess.HTTPRequest{
		{Method: "POST", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=a"},
		{Method: "GET", Path: "/api/plugins/" + pluginID + "/unknown", RawQuery: "session_id=a"},
		{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=a", SessionID: "other"},
		{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=a", Body: []byte("x")},
	} {
		got, err := p.HTTPHandle(t.Context(), call)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status < 400 {
			t.Fatal("invalid call accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached host")
	}
	if got := callHTTP(t, &diagnosticsPlugin{}, "session_id=a"); got.Status != 503 {
		t.Fatal("absent grant not unavailable")
	}
}

func TestInitRejectsContentAndExtraAuthority(t *testing.T) {
	if _, err := (&diagnosticsPlugin{}).Init(t.Context(), subprocess.InitParams{}); err == nil {
		t.Fatal("absent grant accepted")
	}
	for _, mutation := range []func(*pluginapi.QueryGrant){
		func(g *pluginapi.QueryGrant) { g.PluginID = "nanite.other" },
		func(g *pluginapi.QueryGrant) { g.Scope.IncludeContent = true },
		func(g *pluginapi.QueryGrant) {
			g.Scope.Resources = append(slicesCopy(resources), pluginapi.QuerySessions)
		},
		func(g *pluginapi.QueryGrant) { g.Scope.Resources = []pluginapi.QueryResource{pluginapi.QueryUsage} },
		func(g *pluginapi.QueryGrant) { g.Token = "secret" },
	} {
		grant := grantFor("http://127.0.0.1:1234")
		mutation(&grant)
		_, err := (&diagnosticsPlugin{}).Init(t.Context(), subprocess.InitParams{Identity: identity(t, grant)})
		if err == nil || strings.Contains(err.Error(), grant.Token) {
			t.Fatal("invalid grant accepted or leaked")
		}
	}
}
func slicesCopy[T any](values []T) []T { return append([]T(nil), values...) }

func TestScopedGrantDoesNotExpandAtHTTPBoundary(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	grant := grantFor(server.URL)
	grant.Scope.AllSessions = false
	grant.Scope.SessionIDs = []string{"allowed"}
	p := &diagnosticsPlugin{}
	initialize(t, p, grant)
	got := callHTTP(t, p, "session_id=outside")
	if got.Status != 200 || !strings.Contains(string(got.Body), "unavailable") || calls.Load() != 0 {
		t.Fatal("outside scope read reached host")
	}
}

func TestResourceFailuresRemainSeparateAndSafe(t *testing.T) {
	fixtures := goldens(t)
	for _, status := range []int{401, 403, 404, 413, 500, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/usage") {
					http.Error(w, "private credential="+grantFor("").Token, status)
					return
				}
				name := "metrics"
				if strings.HasSuffix(r.URL.Path, "/context_slots") {
					name = "missing-capture"
				}
				hostReply(w, r, fixtures[name].Data)
			}))
			defer server.Close()
			p := &diagnosticsPlugin{}
			initialize(t, p, grantFor(server.URL))
			got := callHTTP(t, p, "session_id=session-a")
			var decoded diagnosticsResponse
			if err := json.Unmarshal(got.Body, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Usage.Code != fmt.Sprintf("host_%d", status) || decoded.Metrics.Data == nil || decoded.Slots.Data == nil {
				t.Fatal("resource failure collapsed other data")
			}
			if strings.Contains(string(got.Body), "private") || strings.Contains(string(got.Body), grantFor("").Token) {
				t.Fatal("host error detail leaked")
			}
		})
	}
}

func TestInvalidHostRepliesNeverFabricateAccounting(t *testing.T) {
	fixtures := goldens(t)
	for _, test := range []struct {
		name, resource, body string
		envelope             bool
	}{
		{"null", "usage", "null", false}, {"missing", "usage", "{}", false},
		{"null-field", "usage", strings.Replace(string(fixtures["usage"].Data), `"input_tokens": 2000`, `"input_tokens": null`, 1), false},
		{"duplicate", "usage", `{"input_tokens":1,"input_tokens":2}`, false},
		{"private-slots", "context_slots", `{"available":true,"slots":[{"name":"system","content":"PRIVATE PROMPT"}]}`, false},
		{"null-slots", "context_slots", `{"available":false,"slots":null}`, false},
		{"contradictory-slots", "context_slots", `{"available":false,"turn_id":"old","slots":[]}`, false},
		{"metrics-session", "execution_metrics", `{"metrics":[{"session_id":"other"}],"more":false}`, false},
		{"null-metrics", "execution_metrics", `{"metrics":null,"more":false}`, false},
		{"mismatch", "usage", `{"protocol":1,"resource":"usage","session_id":"other","data":{}}`, true},
		{"oversized", "usage", strings.Repeat("x", pluginapi.MaxQueryResponseBytes+1), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resource := strings.TrimPrefix(r.URL.Path, "/api/plugin-host/query/")
				if resource == test.resource {
					if test.envelope {
						_, _ = w.Write([]byte(test.body))
					} else {
						hostReply(w, r, json.RawMessage(test.body))
					}
					return
				}
				name := map[string]string{"usage": "usage", "execution_metrics": "metrics", "context_slots": "slots"}[resource]
				hostReply(w, r, fixtures[name].Data)
			}))
			defer server.Close()
			p := &diagnosticsPlugin{}
			initialize(t, p, grantFor(server.URL))
			got := callHTTP(t, p, "session_id=session-a")
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(got.Body, &decoded); err != nil {
				t.Fatal(err)
			}
			var result resourceResult
			if err := json.Unmarshal(decoded[test.resource], &result); err != nil {
				t.Fatal(err)
			}
			if result.Data != nil || result.Error == "" || strings.Contains(string(got.Body), "PRIVATE PROMPT") {
				t.Fatalf("invalid response trusted: %s", decoded[test.resource])
			}
		})
	}
}

func TestLifecycleCancelsInflightReads(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	p := &diagnosticsPlugin{}
	initialize(t, p, grantFor(server.URL))
	finished := make(chan subprocess.HTTPResponse, 1)
	go func() {
		got, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "GET", Path: "/api/plugins/" + pluginID + "/diagnostics", RawQuery: "session_id=session-a"})
		if err != nil {
			t.Error(err)
		}
		finished <- got
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("read did not start")
	}
	if err := p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-finished:
		if got.Status != 200 || !strings.Contains(string(got.Body), `"code":"canceled"`) {
			t.Fatalf("canceled read status %d", got.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unload failed to cancel read")
	}
	if got := callHTTP(t, p, "session_id=session-a"); got.Status != 503 {
		t.Fatal("read after unload")
	}
}

func TestManifestStatesReviewedAuthorityAndPanels(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 0 || len(m.Capabilities) != 1 || !strings.Contains(m.Capabilities[0].Reason, "Workspace-wide READ authority") || !strings.Contains(m.Capabilities[0].Reason, "No content and no mutations") {
		t.Fatal("review authority is not explicit")
	}
	var block pluginapi.Block
	if err := manifest.DecodeExtension(m.Nanite, &block); err != nil {
		t.Fatal(err)
	}
	if err := block.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, panel := range block.Registers.Panels {
		if panel.DefaultVisible {
			t.Fatal("panel visible by default")
		}
	}
	var scope map[string]any
	if err := json.Unmarshal(m.Capabilities[0].Metadata, &scope); err != nil {
		t.Fatal(err)
	}
	if _, present := scope["include_content"]; present || scope["all_sessions"] != true {
		t.Fatal("grant exceeds accounting")
	}
}
