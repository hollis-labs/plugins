package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(raw string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(raw)), Header: make(http.Header)}
}
func TestProviderConfigStrictJSONAndAnchoredMatches(t *testing.T) {
	valid := `[{"name":"Custom","pattern":"^https://custom.example/posts/","endpoint":"https://custom.example/oembed?url={url}"}]`
	providers, err := parseExtraProviders(valid)
	if err != nil || len(providers) != 1 {
		t.Fatal(err)
	}
	if matchProviderIn(providers, "https://custom.example/posts/42") == nil {
		t.Fatal("custom provider missing")
	}
	for _, raw := range []string{`null`, `[] []`, `[{"name":"A","name":"B","pattern":"ok","endpoint":"https://custom.example/{url}"}]`, `[{"name":"Custom","pattern":"[bad","endpoint":"https://custom.example/{url}"}]`, `[{"name":"Custom","pattern":"ok","endpoint":"file:///tmp/{url}"}]`, `[{"name":"Custom","pattern":"ok","endpoint":"https://user:password@custom.example/{url}"}]`, `[{"name":"Custom","pattern":"ok","endpoint":"https://custom.example/{url}{url}"}]`, `[{"name":"Custom","pattern":"ok","endpoint":"https://custom.example/{url}","extra":true}]`, "- name: Old YAML"} {
		if _, err = parseExtraProviders(raw); err == nil {
			t.Fatalf("accepted invalid provider config: %s", raw)
		}
	}
	for _, raw := range []string{"https://evil.example/?url=https://www.youtube.com/watch?v=123", "https://password@www.youtube.com/watch?v=123"} {
		if MatchProvider(raw) != nil {
			t.Fatal("unrelated or credential URL matched provider")
		}
	}
}
func TestEventEnvelopeAndCacheCopiesAreIsolated(t *testing.T) {
	p := &oembedPlugin{}
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pluginapi.DecodeBlock(m.Nanite); err != nil {
		t.Fatal(err)
	}
	init, err := p.Init(context.Background(), subprocess.InitParams{})
	if err != nil || init.ID != m.ID || init.Version != m.Version {
		t.Fatal("identity differs")
	}
	var requests atomic.Int64
	p.state.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		if r.URL.Host != "www.youtube.com" || r.URL.Query().Get("url") != "https://www.youtube.com/watch?v=abc" {
			t.Error("URL changed or sent to wrong provider")
		}
		return response(`{"type":"video","title":"Video title","provider_name":"YouTube","html":"<script>untrusted()</script>"}`, 200), nil
	})}
	request := subprocess.EventRequest{Type: "message.sent", SessionID: "session-one", Data: map[string]any{"content": "See https://www.youtube.com/watch?v=abc"}}
	first, err := p.EventHandle(context.Background(), request)
	if err != nil || len(first.Envelopes) != 1 || first.Envelopes[0].Type != envelopeType {
		t.Fatalf("event: %#v %v", first, err)
	}
	if first.Cancel {
		t.Fatal("post hook canceled message")
	}
	second, err := p.EventHandle(context.Background(), request)
	if err != nil || len(second.Envelopes) != 1 || requests.Load() != 1 {
		t.Fatal("repeat event did not use cache")
	}
	for _, text := range []string{"!giphy https://www.youtube.com/watch?v=abc", "/giphy https://www.youtube.com/watch?v=abc"} {
		request.Data["content"] = text
		result, callErr := p.EventHandle(context.Background(), request)
		if callErr != nil || len(result.Envelopes) != 0 {
			t.Fatal("command triggered preview")
		}
	}
	request.PreHook = true
	request.Data["content"] = "https://www.youtube.com/watch?v=abc"
	result, err := p.EventHandle(context.Background(), request)
	if err != nil || len(result.Envelopes) != 0 {
		t.Fatal("pre-hook produced preview")
	}
	cache := newOEmbedCache(time.Minute)
	original := &OEmbedResult{Title: "Saved"}
	cache.set("key", original)
	original.Title = "Changed"
	cached, _ := cache.get("key")
	cached.Title = "Changed again"
	cached, _ = cache.get("key")
	if cached.Title != "Saved" {
		t.Fatal("caller mutated shared cache")
	}
}
func TestFetchResponseBoundsAndProviderCacheKey(t *testing.T) {
	provider := &Provider{Name: "Fixture", URLPatterns: []*regexp.Regexp{regexp.MustCompile("example")}, Endpoint: "https://provider.example/oembed?url={url}"}
	cache := newOEmbedCache(time.Minute)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"type":"photo","url":"https://images.example/photo.jpg","title":"Photo"}`, 200), nil
	})}
	result, err := FetchOEmbed(context.Background(), client, cache, provider, "https://example.com/post")
	if err != nil || result.ThumbnailURL != "https://images.example/photo.jpg" {
		t.Fatal("photo fallback missing")
	}
	changed := *provider
	changed.Endpoint = "https://another.example/oembed?url={url}"
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(strings.Repeat("x", 128*1024+1), 200), nil
	})
	if _, err = FetchOEmbed(context.Background(), client, cache, &changed, "https://example.com/post"); err == nil {
		t.Fatal("provider change reused old cache or oversized response accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = FetchOEmbed(canceled, client, cache, provider, "https://example.com/post"); !errors.Is(err, context.Canceled) {
		t.Fatal("cache ignored cancellation")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				key := fmt.Sprintf("%d-%d", i, j)
				cache.set(key, &OEmbedResult{Title: key})
				_, _ = cache.get(key)
			}
		}(i)
	}
	wg.Wait()
	if len(cache.entries) > 512 {
		t.Fatal("cache exceeds bound")
	}
}
func TestConcurrentEventsAndUnload(t *testing.T) {
	p := &oembedPlugin{}
	if _, err := p.Init(context.Background(), subprocess.InitParams{}); err != nil {
		t.Fatal(err)
	}
	p.state.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"type":"link","title":"Link"}`, 200), nil
	})}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.EventHandle(context.Background(), subprocess.EventRequest{Type: "message.sent", Data: map[string]any{"content": "https://www.youtube.com/watch?v=abc"}})
		}()
	}
	if err := p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if p.state != nil {
		t.Fatal("unload retained active state")
	}
}
