// oEmbed attaches provider link previews without copying core messages.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	plugin "github.com/hollis-labs/plugin-sdk"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID     = "oembed"
	version      = "0.2.0"
	envelopeType = "oembed-card"
)

var urlRegex = regexp.MustCompile(`https?://[^\s<>"]+`)

type oembedState struct {
	providers []*Provider
	client    *http.Client
	cache     *oEmbedCache
}
type oembedPlugin struct {
	mu    sync.RWMutex
	state *oembedState
}

func (p *oembedPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	providers := append([]*Provider(nil), DefaultProviders...)
	if extra := strings.TrimSpace(params.Config["oembed_extra_providers"]); extra != "" {
		parsed, err := parseExtraProviders(extra)
		if err != nil {
			return subprocess.InitResult{}, fmt.Errorf("oembed_extra_providers: %w", err)
		}
		providers = append(providers, parsed...)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	state := &oembedState{providers: providers, cache: newOEmbedCache(10 * time.Minute), client: &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.mu.Lock()
	p.state = state
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "oEmbed Rich Previews", Version: version, Description: "Link previews from known or configured oEmbed providers", Protocol: subprocess.ProtocolVersion}, nil
}
func (p *oembedPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *oembedPlugin) Unload(context.Context) error {
	p.mu.Lock()
	state := p.state
	p.state = nil
	p.mu.Unlock()
	if state != nil {
		state.client.CloseIdleConnections()
	}
	return nil
}
func (p *oembedPlugin) EventHandle(ctx context.Context, req subprocess.EventRequest) (subprocess.EventResult, error) {
	if req.Type != "message.sent" || req.PreHook {
		return subprocess.EventResult{}, nil
	}
	if err := ctx.Err(); err != nil {
		return subprocess.EventResult{}, err
	}
	content, _ := req.Data["content"].(string)
	content = strings.TrimSpace(content)
	if content == "" || strings.HasPrefix(content, "!") || strings.HasPrefix(content, "/") {
		return subprocess.EventResult{}, nil
	}
	p.mu.RLock()
	state := p.state
	p.mu.RUnlock()
	if state == nil {
		return subprocess.EventResult{}, fmt.Errorf("oembed: plugin unavailable")
	}
	for _, raw := range urlRegex.FindAllString(content, 5) {
		raw = strings.TrimRight(raw, ",.;!)]}")
		if !validContentURL(raw) {
			continue
		}
		provider := matchProviderIn(state.providers, raw)
		if provider == nil {
			continue
		}
		result, err := FetchOEmbed(ctx, state.client, state.cache, provider, raw)
		if err != nil || result == nil {
			if ctx.Err() != nil {
				return subprocess.EventResult{}, ctx.Err()
			}
			continue
		}
		return subprocess.EventResult{Envelopes: []plugin.EnvelopeOut{{Type: envelopeType, Data: map[string]any{"title": result.Title, "description": result.Description, "thumbnail_url": result.ThumbnailURL, "provider_name": result.ProviderName, "provider_url": result.ProviderURL, "type": result.Type, "url": result.URL, "author_name": result.AuthorName, "html": result.HTML}}}}, nil
	}
	return subprocess.EventResult{}, nil
}
func declaration() (manifest.Manifest, error) {
	block, err := pluginapi.EncodeBlock(pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Envelopes: []pluginapi.Envelope{{Type: envelopeType, Component: "OEmbedCard", Version: 1, Schema: "schemas/oembed-card.schema.json"}}, Events: []pluginapi.Event{{Types: []string{"message.sent"}}},
	}})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "oEmbed Rich Previews", Description: "Preview matching chat URLs by sending them to their known or configured oEmbed provider", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/oembed"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: block,
		Config: manifest.Config{Fields: map[string]manifest.Field{"oembed_extra_providers": {Type: "string", Label: "Extra providers", Description: "JSON array of name, pattern and endpoint triples; endpoint includes {url}. Matching URLs are sent to this endpoint."}}},
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
		err = subprocess.Serve(&oembedPlugin{})
	} else {
		err = fmt.Errorf("usage: oembed [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
