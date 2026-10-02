package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func fixtureIdentity(t *testing.T, host string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"nanite_durable_wake": pluginapi.DurableWakeGrant{Protocol: pluginapi.DurableWakeProtocol, PluginID: pluginID, HostURL: host, Token: base64.RawURLEncoding.EncodeToString(make([]byte, 32)), Scope: pluginapi.DurableWakeScope{AgentSlugs: []string{"loom-curator"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestCallbackDeliversIdentityInRealPrompt(t *testing.T) {
	var requests atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/plugin-host/durable-wake" || r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("wake wire changed")
		}
		var request pluginapi.DurableWakeRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.AgentSlug != "loom-curator" || request.Reason != "callback:wiki_page" || request.Facts["fragment_id"] != "fragment-one" || !strings.Contains(request.Prompt, "Run your classify_and_compile_fragment procedure now.") || !strings.Contains(request.Prompt, "fragment_id: fragment-one") || !strings.Contains(request.Prompt, "fetch the fragment's real content yourself") {
			t.Error("fragment identity did not become real prompt")
		}
		_, _ = w.Write([]byte(`{"protocol":1,"agent_slug":"loom-curator","instance_id":"instance-one","session_id":"session-one","status":"queued"}`))
	}))
	defer host.Close()
	p := &loomPlugin{}
	init, err := p.Init(context.Background(), subprocess.InitParams{Identity: fixtureIdentity(t, host.URL)})
	if err != nil || init.ID != pluginID || init.Version != version {
		t.Fatal(init, err)
	}
	call := subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/curator-wake", Body: []byte(`{"generator":"wiki_page","fragment":{"id":"fragment-one","title":"A finding","source":"fe","source_type":"fragment","source_id":"source-one","canonical_path":"wiki/page"}}`)}
	result, err := p.HTTPHandle(context.Background(), call)
	if err != nil || result.Status != 200 || !bytes.Contains(result.Body, []byte(`"status":"queued"`)) || requests.Load() != 1 {
		t.Fatal(result, err)
	}
	for _, body := range []string{`{}`, `{"fragment":{"id":""}}`, `{"fragment":{"id":"fragment-one","body":"private body"}}`, `{"fragment":{"id":"fragment-one","id":"fragment-two"}}`, strings.Repeat("x", 32*1024+1)} {
		call.Body = []byte(body)
		result, err = p.HTTPHandle(context.Background(), call)
		if err != nil || (result.Status != 400 && result.Status != 413) || requests.Load() != 1 {
			t.Fatal("invalid callback reached host", result, err)
		}
	}
	if err = p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	call.Body = []byte(`{"generator":"wiki_page","fragment":{"id":"fragment-one"}}`)
	result, err = p.HTTPHandle(context.Background(), call)
	if err != nil || result.Status != 503 || requests.Load() != 1 {
		t.Fatal("unloaded plugin woke curator")
	}
}
func TestManifestSeedsAndGrantRefusal(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	block, err := pluginapi.DecodeBlock(m.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Registers.ReflexSeeds) != 3 || block.UI.Bundle != "" {
		t.Fatal("headless contribution changed")
	}
	scope, err := pluginapi.ReflexScopeFor(block, m.Capabilities)
	if err != nil || len(scope.SeedIDs) != 3 {
		t.Fatal(scope, err)
	}
	seen := map[string]bool{}
	for _, seed := range block.Registers.ReflexSeeds {
		if seed.AgentSlug != "loom-curator" && seed.AgentSlug != "loom-weaver" {
			t.Fatal("portfolio-wide seed")
		}
		if seed.Validate() != nil || seed.Trigger.Kind != "AND" || len(seed.Trigger.Clauses) != 2 || seed.Reminder == "" || seen[seed.ID] {
			t.Fatal("seed semantics changed")
		}
		seen[seed.ID] = true
	}
	p := &loomPlugin{}
	if _, err = p.Init(context.Background(), subprocess.InitParams{}); err == nil {
		t.Fatal("wake without grant")
	}
	raw := bytes.ReplaceAll(fixtureIdentity(t, "http://127.0.0.1:8090"), []byte(pluginID), []byte("intruder"))
	if _, err = p.Init(context.Background(), subprocess.InitParams{Identity: raw}); err == nil {
		t.Fatal("foreign owner accepted")
	}
}
func TestConcurrentCallbackUnload(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"protocol":1,"agent_slug":"loom-curator","instance_id":"instance-one","status":"queued"}`))
	}))
	defer host.Close()
	p := &loomPlugin{}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Identity: fixtureIdentity(t, host.URL)}); err != nil {
		t.Fatal(err)
	}
	var calls sync.WaitGroup
	for range 16 {
		calls.Add(1)
		go func() {
			defer calls.Done()
			_, _ = p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: "/api/plugins/" + pluginID + "/curator-wake", Body: []byte(`{"fragment":{"id":"fragment-one"}}`)})
		}()
	}
	if err := p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls.Wait()
}
