package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID = "nanite.loom"
	version  = "0.1.0"
)

type loomPlugin struct {
	mu     sync.RWMutex
	client *pluginapi.DurableWakeClient
}

func (p *loomPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.DurableWakeGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID {
		return subprocess.InitResult{}, fmt.Errorf("loom: wake grant owner mismatch")
	}
	client, err := pluginapi.NewDurableWakeClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Loom integration", Version: version, Description: "Fragment callbacks and Loom reminder declarations", Protocol: subprocess.ProtocolVersion}, nil
}
func (*loomPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *loomPlugin) Unload(context.Context) error {
	p.mu.Lock()
	p.client = nil
	p.mu.Unlock()
	return nil
}
func (p *loomPlugin) HTTPHandle(ctx context.Context, call subprocess.HTTPRequest) (subprocess.HTTPResponse, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /curator-wake", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil {
			http.Error(w, "callback exceeds limit", http.StatusRequestEntityTooLarge)
			return
		}
		var callback LoomCuratorWakeRequest
		if err = manifest.DecodeExtension(raw, &callback); err != nil || strings.TrimSpace(callback.Fragment.ID) == "" {
			http.Error(w, "invalid fragment callback", 400)
			return
		}
		if len(callback.Fragment.ID) > 128 || len(callback.Generator) > 128 {
			http.Error(w, "fragment identity exceeds limit", 400)
			return
		}
		generator := strings.TrimSpace(callback.Generator)
		reason := "callback"
		if generator != "" {
			reason += ":" + generator
		}
		facts := map[string]string{"generator": generator, "fragment_id": callback.Fragment.ID, "fragment_source": callback.Fragment.Source, "fragment_source_type": callback.Fragment.SourceType, "fragment_source_id": callback.Fragment.SourceID, "fragment_title": callback.Fragment.Title, "fragment_canonical_path": callback.Fragment.CanonicalPath}
		for key, value := range facts {
			if value == "" {
				delete(facts, key)
			}
		}
		request := pluginapi.DurableWakeRequest{AgentSlug: "loom-curator", Reason: reason, Prompt: buildLoomCuratorWakePrompt(generator, callback.Fragment), Facts: facts}
		if err = request.Validate(); err != nil {
			http.Error(w, "invalid fragment identity", 400)
			return
		}
		p.mu.RLock()
		client := p.client
		p.mu.RUnlock()
		if client == nil {
			http.Error(w, "Loom integration unavailable", http.StatusServiceUnavailable)
			return
		}
		response, err := client.Wake(r.Context(), request)
		if err != nil {
			http.Error(w, "curator wake failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	return pluginapi.HandleHTTP(ctx, pluginID, mux, call)
}
func declaration() (manifest.Manifest, error) {
	block := pluginapi.Block{Registers: pluginapi.Registrations{HTTPRoutes: []pluginapi.Route{{Method: "POST", Path: "curator-wake"}}, ReflexSeeds: loomReflexSeeds()}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Loom integration", Description: "Translate Fragments Engine callbacks into scoped curator wakes and contribute Loom reminder defaults", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/loom"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityDurableWake, Reason: "Wake the existing Loom Curator with callback fragment identity", Metadata: json.RawMessage(`{"agent_slugs":["loom-curator"]}`)}, {Name: pluginapi.CapabilityReflexSeed, Reason: "Contribute opt-out-able reminder defaults for Curator and Weaver", Metadata: json.RawMessage(`{"seed_ids":["check-before-answer","weaver-capture-on-discovery","curator-capture-on-discovery"],"agent_slugs":["loom-curator","loom-weaver"]}`)}},
	}
	if _, err = pluginapi.ReflexScopeFor(block, m.Capabilities); err != nil {
		return m, err
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
		err = subprocess.Serve(&loomPlugin{})
	} else {
		err = fmt.Errorf("usage: loom [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
