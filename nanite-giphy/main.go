// Giphy searches GIFs using the public subprocess contracts.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	plugin "github.com/hollis-labs/plugin-sdk"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID     = "giphy"
	version      = "0.2.0"
	envelopeType = "giphy-modal"
)

type giphyState struct {
	apiKey, rating string
	client         *http.Client
}
type giphyPlugin struct {
	mu    sync.RWMutex
	state *giphyState
}

func (p *giphyPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	rating := params.Config["giphy_rating"]
	if rating == "" {
		rating = "g"
	}
	switch rating {
	case "g", "pg", "pg-13", "r":
	default:
		return subprocess.InitResult{}, fmt.Errorf("giphy: invalid rating")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	state := &giphyState{apiKey: params.Config["giphy_api_key"], rating: rating, client: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Giphy", Version: version, Description: "Giphy GIF search", Protocol: subprocess.ProtocolVersion}, nil
}
func (p *giphyPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *giphyPlugin) Unload(context.Context) error {
	p.mu.Lock()
	state := p.state
	p.state = nil
	p.mu.Unlock()
	if state != nil {
		state.client.CloseIdleConnections()
	}
	return nil
}
func (p *giphyPlugin) search(ctx context.Context, query string) (*Result, error) {
	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()
	if state == nil {
		return nil, fmt.Errorf("giphy: plugin unavailable")
	}
	return Search(ctx, state.client, state.apiKey, state.rating, query)
}
func resultEnvelope(result *Result) plugin.EnvelopeOut {
	return plugin.EnvelopeOut{Type: envelopeType, Data: map[string]any{"title": result.Title, "gif_url": result.GifURL, "source": result.Source, "query": result.Query}}
}
func (p *giphyPlugin) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
	if req.Name != "giphy" {
		return subprocess.CommandResult{}, fmt.Errorf("unknown command")
	}
	result, err := p.search(ctx, req.Args)
	if err != nil {
		return subprocess.CommandResult{Action: "error", Content: err.Error()}, nil
	}
	if result == nil {
		return subprocess.CommandResult{Action: "message", Content: "No GIFs found."}, nil
	}
	return subprocess.CommandResult{Action: "message", Content: result.Title, Envelopes: []plugin.EnvelopeOut{resultEnvelope(result)}}, nil
}
func toolContent(value any, isError bool) (subprocess.MCPCallResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": string(raw)}})
	return subprocess.MCPCallResult{Content: content, IsError: isError}, err
}
func (p *giphyPlugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	if req.ToolName != "giphy_search" {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool")
	}
	query, ok := req.Arguments["query"].(string)
	if !ok {
		return toolContent(map[string]string{"error": "query is required"}, true)
	}
	result, err := p.search(ctx, query)
	if err != nil {
		return toolContent(map[string]string{"error": err.Error()}, true)
	}
	if result == nil {
		return toolContent(map[string]bool{"found": false}, false)
	}
	output, err := toolContent(result, false)
	output.Envelopes = []plugin.EnvelopeOut{resultEnvelope(result)}
	return output, err
}
func declaration() (manifest.Manifest, error) {
	block, err := pluginapi.EncodeBlock(pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Envelopes: []pluginapi.Envelope{{Type: envelopeType, Component: "GiphyModalCard", Version: 1, Schema: "schemas/giphy-modal.schema.json"}},
		Commands:  []pluginapi.Command{{Name: "giphy", Description: "Search GIFs: /giphy <query>"}},
	}})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Giphy", Description: "Search Giphy for inline GIFs; demo results when no API key is configured", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/giphy"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: block,
		Config: manifest.Config{Fields: map[string]manifest.Field{"giphy_rating": {Type: "select", Label: "Maximum rating", Default: "g", Options: []string{"g", "pg", "pg-13", "r"}}}, Secrets: map[string]manifest.Secret{"giphy_api_key": {Label: "Giphy API key", Description: "Optional; built-in demo GIFs when unset", Env: "GIPHY_API_KEY"}}},
		Tools:  []manifest.Tool{{Name: "giphy_search", Description: "Search for a GIF and attach an inline card", Effect: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":1024}},"required":["query"],"additionalProperties":false}`)}},
	}
	return m, m.Validate()
}
func main() {
	var err error
	if len(os.Args) == 2 && os.Args[1] == "--manifest" {
		var m manifest.Manifest
		m, err = declaration()
		if err == nil {
			err = manifest.Encode(os.Stdout, m)
		}
	} else if len(os.Args) == 1 {
		err = subprocess.Serve(&giphyPlugin{})
	} else {
		err = fmt.Errorf("usage: giphy [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
