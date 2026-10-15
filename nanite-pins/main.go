package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID = "nanite.pins"
	version  = "0.2.0"
)

var _ subprocess.MCPHandler = (*pinsPlugin)(nil)
var _ subprocess.HTTPHandler = (*pinsPlugin)(nil)

type pinsPlugin struct {
	mu     sync.Mutex
	client *pluginapi.QueryClient
	db     database
	source string
	closed bool
}

func (p *pinsPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID {
		return subprocess.InitResult{}, fmt.Errorf("pins: query grant owner differs")
	}
	client, err := pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if params.DataDir == "" {
		return subprocess.InitResult{}, fmt.Errorf("pins: DataDir required")
	}
	if err = os.MkdirAll(params.DataDir, 0700); err != nil {
		return subprocess.InitResult{}, err
	}
	p.mu.Lock()
	p.client = client
	p.db = database{dir: params.DataDir}
	p.closed = false
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Pins", Version: version, Description: "Durable session and project pins", Protocol: subprocess.ProtocolVersion}, nil
}
func (*pinsPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *pinsPlugin) Unload(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}
func (p *pinsPlugin) ready(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client == nil {
		return "", fmt.Errorf("pins unavailable")
	}
	if p.source != "" {
		return p.source, nil
	}
	receipts, err := p.client.ExportReceipts(ctx, 100)
	if err != nil {
		return "", err
	}
	if receipts.More {
		return "", fmt.Errorf("incomplete receipt list")
	}
	var receipt *pluginapi.DataExportReceipt
	for _, candidate := range receipts.Exports {
		if candidate.Feature != "pins" {
			continue
		}
		if receipt != nil {
			return "", fmt.Errorf("ambiguous workspace receipt")
		}
		copy := candidate
		receipt = &copy
	}
	if receipt == nil {
		return "", fmt.Errorf("waiting for committed core import")
	}
	if err = p.db.importReceipt(ctx, *receipt); err != nil {
		return "", err
	}
	p.source = receipt.SourceID
	return p.source, nil
}
func (p *pinsPlugin) session(ctx context.Context, id string) (pluginapi.QuerySession, error) {
	if _, err := p.ready(ctx); err != nil {
		return pluginapi.QuerySession{}, err
	}
	result, err := p.client.Query(ctx, pluginapi.QueryRequest{Resource: pluginapi.QuerySessions, SessionID: id, Limit: 1})
	if err != nil {
		return pluginapi.QuerySession{}, err
	}
	var sessions pluginapi.QuerySessionsData
	if err = manifest.DecodeExtension(result.Data, &sessions); err != nil {
		return pluginapi.QuerySession{}, err
	}
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != id {
		return pluginapi.QuerySession{}, fmt.Errorf("current session unavailable")
	}
	return sessions.Sessions[0], nil
}

type createRequest struct {
	Content string `json:"content"`
	Scope   string `json:"scope,omitempty"`
}

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" || len(content) > 8192 || strings.ContainsRune(content, 0) || !utf8.ValidString(content) {
		return fmt.Errorf("content must contain 1 to 8192 UTF-8 bytes without NUL")
	}
	return nil
}
func (p *pinsPlugin) create(ctx context.Context, session string, req createRequest) (pin, error) {
	if err := validateContent(req.Content); err != nil {
		return pin{}, err
	}
	if req.Scope == "" {
		req.Scope = "session"
	}
	if req.Scope != "session" && req.Scope != "project" {
		return pin{}, fmt.Errorf("pins require session or project scope")
	}
	source, err := p.ready(ctx)
	if err != nil {
		return pin{}, err
	}
	meta, err := p.session(ctx, session)
	if err != nil {
		return pin{}, err
	}
	projectID := ""
	if req.Scope == "project" {
		projectID = meta.ProjectID
		if projectID == "" {
			return pin{}, fmt.Errorf("current session has no project")
		}
	}
	return p.db.create(ctx, source, pin{SessionID: &session, Scope: req.Scope, ProjectID: projectID, Content: req.Content})
}
func (p *pinsPlugin) MCPCallTool(ctx context.Context, call subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	meta, err := p.session(ctx, call.SessionID)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	var value any
	switch call.ToolName {
	case "pins_set":
		raw, marshalErr := json.Marshal(call.Arguments)
		if marshalErr != nil {
			return subprocess.MCPCallResult{}, marshalErr
		}
		var req createRequest
		if decodeErr := manifest.DecodeExtension(raw, &req); decodeErr != nil {
			return subprocess.MCPCallResult{}, decodeErr
		}
		value, err = p.create(ctx, call.SessionID, req)
	case "pins_list":
		return p.listInventory(ctx, source, call.SessionID, meta.ProjectID, call.Arguments)
	case "pins_get":
		return p.getContent(ctx, source, call.SessionID, meta.ProjectID, call.Arguments)
	case "pins_delete":
		id, valid := call.Arguments["id"].(string)
		if !valid || id == "" || len(call.Arguments) != 1 {
			return subprocess.MCPCallResult{}, fmt.Errorf("id required")
		}
		err = p.db.change(ctx, source, call.SessionID, meta.ProjectID, id, "delete", "")
		value = map[string]string{"id": id, "status": "deleted"}
	case "pins_update":
		var req struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		raw, marshalErr := json.Marshal(call.Arguments)
		if marshalErr != nil {
			return subprocess.MCPCallResult{}, marshalErr
		}
		if decodeErr := manifest.DecodeExtension(raw, &req); decodeErr != nil || req.ID == "" {
			return subprocess.MCPCallResult{}, fmt.Errorf("id and content required")
		}
		if checkErr := validateContent(req.Content); checkErr != nil {
			return subprocess.MCPCallResult{}, checkErr
		}
		err = p.db.change(ctx, source, call.SessionID, meta.ProjectID, req.ID, "content", req.Content)
		value = map[string]string{"id": req.ID, "status": "updated"}
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown pin tool")
	}
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": string(raw)}})
	return subprocess.MCPCallResult{Content: content}, err
}
func declaration() (manifest.Manifest, error) {
	block := pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Slots: []pluginapi.Slot{{ID: "pins", Slot: pluginapi.SlotWorkingDrawer, Component: "PinsTab", Title: "Pins", Icon: "Pin", Priority: 30}}, AlwaysShipSources: []pluginapi.AlwaysShipSource{{ID: "pins", Title: "Pinned Context", ListTool: "pins_list"}},
		HTTPRoutes: []pluginapi.Route{{Method: "GET", Path: "pins"}, {Method: "POST", Path: "pins"}, {Method: "PATCH", Path: "pins/"}, {Method: "DELETE", Path: "pins/"}},
	}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	query, err := json.Marshal(pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryDataExports}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	scope, err := json.Marshal(pluginapi.AlwaysShipScope{SourceIDs: []string{"pins"}, AllSessions: true, MaxBytes: pluginapi.MaxAlwaysShipBodyBytes})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Pins", Description: "Durable pinned context with session/project scope", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/pins"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Import committed pins exports and resolve session/project metadata; no message content", Metadata: query}, {Name: pluginapi.CapabilityContextAlwaysShip, Reason: "Retain visible pins on every turn, including review, recall and resume; survives compaction and shares user-context budget", Metadata: scope}},
		Tools: []manifest.Tool{
			{Name: "pins_set", Effect: "write", Description: "Pin content in the calling session or project; persists until explicit deletion", InputSchema: json.RawMessage(`{"type":"object","properties":{"content":{"type":"string","maxLength":8192},"scope":{"enum":["session","project"]}},"required":["content"],"additionalProperties":false}`)},
			{Name: "pins_list", Effect: "read", Description: "List bounded visible pin inventory; call with no arguments for first page, follow next_offset while more, then pins_get for content (agent tool grants required)", InputSchema: json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0,"maximum":1000000}},"additionalProperties":false}`)},
			{Name: "pins_get", Effect: "read", Description: "Read a visible pin in bounded UTF-8 chunks; offset is a content byte offset, follow next_offset while more; total_bytes is the complete content size", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string","minLength":1},"offset":{"type":"integer","minimum":0}},"required":["id"],"additionalProperties":false}`)},
			{Name: "pins_update", Effect: "write", Description: "Edit the content of a visible pin", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"content":{"type":"string","maxLength":8192}},"required":["id","content"],"additionalProperties":false}`)},
			{Name: "pins_delete", Effect: "write", Description: "Delete a visible pin", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		}}
	if _, err = pluginapi.AlwaysShipScopeFor(block, m.Capabilities, m.Tools); err != nil {
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
		err = subprocess.Serve(&pinsPlugin{})
	} else {
		err = fmt.Errorf("usage: pins [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
