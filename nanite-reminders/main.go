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
	pluginID = "nanite.reminders"
	version  = "0.1.0"
)

var _ subprocess.MCPHandler = (*remindersPlugin)(nil)
var _ subprocess.HTTPHandler = (*remindersPlugin)(nil)

type remindersPlugin struct {
	mu     sync.Mutex
	client *pluginapi.QueryClient
	db     database
	source string
	closed bool
}

func (p *remindersPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID {
		return subprocess.InitResult{}, fmt.Errorf("reminders: query grant owner differs")
	}
	client, err := pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if params.DataDir == "" {
		return subprocess.InitResult{}, fmt.Errorf("reminders: DataDir required")
	}
	if err = os.MkdirAll(params.DataDir, 0700); err != nil {
		return subprocess.InitResult{}, err
	}
	p.mu.Lock()
	p.client = client
	p.db = database{dir: params.DataDir}
	p.closed = false
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Reminders", Version: version, Description: "Durable reminders with explicit acknowledgment", Protocol: subprocess.ProtocolVersion}, nil
}
func (*remindersPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *remindersPlugin) Unload(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}
func (p *remindersPlugin) ready(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client == nil {
		return "", fmt.Errorf("reminders unavailable")
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
		if candidate.Feature != "reminders" {
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
func (p *remindersPlugin) session(ctx context.Context, id string) (pluginapi.QuerySession, int, error) {
	if _, err := p.ready(ctx); err != nil {
		return pluginapi.QuerySession{}, 0, err
	}
	result, err := p.client.Query(ctx, pluginapi.QueryRequest{Resource: pluginapi.QuerySessions, SessionID: id, Limit: 1})
	if err != nil {
		return pluginapi.QuerySession{}, 0, err
	}
	var sessions pluginapi.QuerySessionsData
	if err = manifest.DecodeExtension(result.Data, &sessions); err != nil {
		return pluginapi.QuerySession{}, 0, err
	}
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != id {
		return pluginapi.QuerySession{}, 0, fmt.Errorf("current session not found")
	}
	usage, err := p.client.Query(ctx, pluginapi.QueryRequest{Resource: pluginapi.QueryUsage, SessionID: id, Limit: 1})
	if err != nil {
		return pluginapi.QuerySession{}, 0, err
	}
	var data pluginapi.QueryUsageData
	if err = manifest.DecodeExtension(usage.Data, &data); err != nil {
		return pluginapi.QuerySession{}, 0, err
	}
	if data.MessageCount < 0 {
		return pluginapi.QuerySession{}, 0, fmt.Errorf("invalid session message count")
	}
	return sessions.Sessions[0], data.MessageCount, nil
}

type createRequest struct {
	Text    string          `json:"text"`
	Trigger json.RawMessage `json:"trigger"`
	Scope   string          `json:"scope,omitempty"`
}

func (p *remindersPlugin) create(ctx context.Context, session string, req createRequest) (reminder, error) {
	if strings.TrimSpace(req.Text) == "" || len(req.Text) > 8192 || strings.ContainsRune(req.Text, 0) || !utf8.ValidString(req.Text) {
		return reminder{}, fmt.Errorf("text must contain 1 to 8192 UTF-8 bytes without NUL")
	}
	parsed, err := parseTrigger(string(req.Trigger))
	if err != nil {
		return reminder{}, err
	}
	raw, err := json.Marshal(parsed)
	if err != nil {
		return reminder{}, err
	}
	if req.Scope == "" {
		req.Scope = "session"
	}
	if req.Scope != "session" && req.Scope != "turn" && req.Scope != "project" {
		return reminder{}, fmt.Errorf("invalid scope")
	}
	source, err := p.ready(ctx)
	if err != nil {
		return reminder{}, err
	}
	meta, count, err := p.session(ctx, session)
	if err != nil {
		return reminder{}, err
	}
	projectID := ""
	if req.Scope == "project" {
		projectID = meta.ProjectID
		if projectID == "" {
			return reminder{}, fmt.Errorf("current session has no project")
		}
	}
	return p.db.create(ctx, source, reminder{SessionID: session, Scope: req.Scope, ProjectID: projectID, Text: req.Text, TriggerJSON: string(raw)}, count)
}
func (p *remindersPlugin) MCPCallTool(ctx context.Context, call subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	meta, _, err := p.session(ctx, call.SessionID)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	var value any
	switch call.ToolName {
	case "reminders_set":
		raw, marshalErr := json.Marshal(call.Arguments)
		if marshalErr != nil {
			return subprocess.MCPCallResult{}, marshalErr
		}
		var req createRequest
		if decodeErr := manifest.DecodeExtension(raw, &req); decodeErr != nil {
			return subprocess.MCPCallResult{}, decodeErr
		}
		value, err = p.create(ctx, call.SessionID, req)
	case "reminders_list":
		var args struct {
			Offset int `json:"offset,omitempty"`
		}
		arguments := call.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		rawArgs, marshalErr := json.Marshal(arguments)
		if marshalErr != nil {
			return subprocess.MCPCallResult{}, marshalErr
		}
		if decodeErr := manifest.DecodeExtension(rawArgs, &args); decodeErr != nil || args.Offset < 0 || args.Offset > pluginapi.MaxDataExportRows {
			return subprocess.MCPCallResult{}, fmt.Errorf("invalid list offset")
		}
		var rows []reminder
		rows, err = p.db.list(ctx, source, call.SessionID, meta.ProjectID)
		start := min(args.Offset, len(rows))
		end := min(start+100, len(rows))
		value = map[string]any{"reminders": rows[start:end], "more": end < len(rows), "next_offset": end}
	case "reminders_ack", "reminders_delete":
		id, valid := call.Arguments["id"].(string)
		if !valid || id == "" || len(call.Arguments) != 1 {
			return subprocess.MCPCallResult{}, fmt.Errorf("id required")
		}
		action := "ack"
		if call.ToolName == "reminders_delete" {
			action = "delete"
		}
		err = p.db.change(ctx, source, call.SessionID, meta.ProjectID, id, action, "")
		value = map[string]string{"id": id, "status": action}
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown reminder tool")
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
		Slots:          []pluginapi.Slot{{ID: "reminders", Slot: pluginapi.SlotWorkingDrawer, Component: "RemindersTab", Title: "Reminders", Icon: "Bell", Priority: 35}},
		ContextSources: []pluginapi.ContextSource{{ID: "reminders"}},
		HTTPRoutes:     []pluginapi.Route{{Method: "GET", Path: "reminders"}, {Method: "POST", Path: "reminders"}, {Method: "POST", Path: "reminders/"}, {Method: "PATCH", Path: "reminders/"}, {Method: "DELETE", Path: "reminders/"}},
	}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	query, err := json.Marshal(pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryUsage, pluginapi.QueryDataExports}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	contextScope, err := json.Marshal(pluginapi.ContextScope{SourceIDs: []string{"reminders"}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Reminders", Description: "Time and turn-count reminders with durable state and explicit acknowledgment", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/reminders"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Import committed reminder exports and resolve current session/project and message-count metadata; no message content", Metadata: query}, {Name: pluginapi.CapabilityContextSource, Reason: "Provide due reminders in approved session context until explicitly acknowledged", Metadata: contextScope}},
		Tools: []manifest.Tool{
			{Name: "reminders_set", Effect: "write", Description: "Set a time or turn-count reminder in the calling session/project. Due reminders repeat until reminders_ack.", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","maxLength":8192},"trigger":{"type":"object","properties":{"type":{"enum":["time","turn_count"]},"at":{"type":"string"},"n":{"type":"integer","minimum":1,"maximum":1000000}},"required":["type"],"additionalProperties":false},"scope":{"enum":["turn","session","project"]}},"required":["text","trigger"],"additionalProperties":false}`)},
			{Name: "reminders_list", Effect: "read", Description: "List pending session/project reminders, 100 per page", InputSchema: json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0,"maximum":1000000}},"additionalProperties":false}`)},
			{Name: "reminders_ack", Effect: "write", Description: "Acknowledge a handled reminder; stops further context injection", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
			{Name: "reminders_delete", Effect: "write", Description: "Delete a visible reminder", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		}}
	if _, err = pluginapi.ContextScopeFor(block, m.Capabilities); err != nil {
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
		err = subprocess.Serve(&remindersPlugin{})
	} else {
		err = fmt.Errorf("usage: reminders [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
