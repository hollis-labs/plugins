package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(raw string, status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(raw)), Header: make(http.Header)}
}
func TestManifestRuntimeAndCanonicalTool(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pluginapi.DecodeBlock(m.Nanite); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Config.Secrets["giphy_api_key"]; !ok {
		t.Fatal("API key declared as ordinary config")
	}
	p := &giphyPlugin{}
	init, err := p.Init(context.Background(), subprocess.InitParams{})
	if err != nil {
		t.Fatal(err)
	}
	if init.ID != m.ID || init.Version != m.Version || init.Protocol != m.Protocol {
		t.Fatal("runtime and declaration differ")
	}
	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "giphy_search", Arguments: map[string]any{"query": "cats coding"}})
	if err != nil || result.IsError || len(result.Envelopes) != 1 || !json.Valid(result.Content) {
		t.Fatalf("declared tool failed: %#v %v", result, err)
	}
	if result.Envelopes[0].Type != envelopeType {
		t.Fatal("envelope differs from manifest")
	}
	first := searchDemo("cats coding")
	for i := 0; i < 50; i++ {
		if searchDemo("cats coding").GifURL != first.GifURL {
			t.Fatal("demo selection varies with map iteration")
		}
	}
	if _, err = p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "search"}); err == nil {
		t.Fatal("accepted retired tool name")
	}
	if _, err = p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{"giphy_rating": "unknown"}}); err == nil {
		t.Fatal("accepted invalid rating")
	}
}
func TestLiveSearchBoundsAndSecretFailures(t *testing.T) {
	const secret = "secret-never-in-errors"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.giphy.com" || r.URL.Query().Get("api_key") != secret || r.URL.Query().Get("q") != "hello & goodbye" || r.URL.Query().Get("rating") != "pg" {
			t.Fatal("request changed approved fields")
		}
		return response(`{"data":[{"images":{"original":{"url":"https://media.giphy.com/test.gif"}}}]}`, 200), nil
	})}
	result, err := Search(context.Background(), client, secret, "pg", "hello & goodbye")
	if err != nil || result.GifURL != "https://media.giphy.com/test.gif" {
		t.Fatalf("search: %#v %v", result, err)
	}
	for _, raw := range []string{strings.Repeat("x", 256*1024+1), `{"data":[{"images":{"original":{"url":"javascript:alert(1)"}}}]}`, `{"data":[{"images":{"original":{"url":"https://evil.example/test.gif"}}}]}`} {
		client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(raw, 200), nil })
		if _, err = Search(context.Background(), client, secret, "g", "cats"); err == nil {
			t.Fatal("accepted invalid or oversized response")
		}
	}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	if _, err = Search(context.Background(), client, secret, "g", "cats"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("request failure leaked secret")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Search(canceled, client, "", "g", "cats"); !errors.Is(err, context.Canceled) {
		t.Fatal("demo ignored canceled context")
	}
}
func TestConcurrentLifecycle(t *testing.T) {
	p := &giphyPlugin{}
	if _, err := p.Init(context.Background(), subprocess.InitParams{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Command(context.Background(), subprocess.CommandRequest{Name: "giphy", Args: "cats"})
		}()
	}
	if err := p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := p.search(context.Background(), "cats"); err == nil {
		t.Fatal("unloaded plugin executed")
	}
}
