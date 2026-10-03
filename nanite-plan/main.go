package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const pluginID = "nanite.plan"
const version = "0.1.0"
const maxResponseBytes = 2 << 20

type planPlugin struct {
	mu     sync.Mutex
	client *pluginapi.QueryClient
	db     database
	source string
	closed bool
}

func (p *planPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID || params.DataDir == "" {
		return subprocess.InitResult{}, fmt.Errorf("plan: owner grant and DataDir required")
	}
	client, err := pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if err = os.MkdirAll(params.DataDir, 0700); err != nil {
		return subprocess.InitResult{}, err
	}
	p.mu.Lock()
	p.client = client
	p.db = newDatabase(params.DataDir)
	p.source = ""
	p.closed = false
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Plan", Version: version, Description: "Durable todos and plans", Protocol: subprocess.ProtocolVersion}, nil
}
func (*planPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *planPlugin) Unload(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}

// No plugin mutex is held across host I/O or the cancellable file lock.
func (p *planPlugin) ready(ctx context.Context) (string, error) {
	p.mu.Lock()
	source, client, db, closed := p.source, p.client, p.db, p.closed
	p.mu.Unlock()
	if closed || client == nil {
		return "", errUnavailable
	}
	if source != "" {
		return source, nil
	}
	receipts, err := client.ExportReceipts(ctx, 100)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errUnavailable, err)
	}
	if receipts.More {
		return "", fmt.Errorf("%w: incomplete receipts", errUnavailable)
	}
	source, err = db.importReceipts(ctx, receipts.Exports)
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client != client {
		return "", errUnavailable
	}
	p.source = source
	return source, nil
}
func (p *planPlugin) session(ctx context.Context, id string) (pluginapi.QuerySession, error) {
	if id == "" {
		return pluginapi.QuerySession{}, nil
	}
	p.mu.Lock()
	client, closed := p.client, p.closed
	p.mu.Unlock()
	if closed || client == nil {
		return pluginapi.QuerySession{}, errUnavailable
	}
	reply, err := client.Query(ctx, pluginapi.QueryRequest{Resource: pluginapi.QuerySessions, SessionID: id, Limit: 1})
	if err != nil {
		return pluginapi.QuerySession{}, err
	}
	var data pluginapi.QuerySessionsData
	if err = manifest.DecodeExtension(reply.Data, &data); err != nil {
		return pluginapi.QuerySession{}, err
	}
	if len(data.Sessions) != 1 || data.Sessions[0].ID != id {
		return pluginapi.QuerySession{}, fmt.Errorf("%w: session unavailable", errInvalid)
	}
	return data.Sessions[0], nil
}
func (p *planPlugin) MCPCallTool(ctx context.Context, call subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	known := false
	for _, tool := range agentTools() {
		if tool.Name == call.ToolName {
			known = true
		}
	}
	if !known {
		return toolError(errInvalid), nil
	}
	// Validate advertised shapes without tightening their additional-properties
	// policy or replacing core's explicit workspace/session/project coordinates.
	args := declaredArguments(call.ToolName, call.Arguments)
	fail := func(err error) (subprocess.MCPCallResult, error) {
		if call.ToolName == "todo_update" {
			return todoUpdateFailure(err), nil
		}
		return toolError(err), nil
	}

	raw, err := json.Marshal(call.Arguments)
	if err != nil {
		return fail(err)
	}
	if len(raw) > 512<<10 {
		return fail(errQuota)
	}
	if err = checkToolArguments(call.ToolName, args); err != nil {
		return fail(err)
	}
	source, err := p.ready(ctx)
	if err != nil {
		return fail(err)
	}
	meta, err := p.session(ctx, call.SessionID)
	if err != nil {
		return fail(err)
	}
	p.mu.Lock()
	db := p.db
	p.mu.Unlock()
	value, err := db.operate(ctx, source, call.ToolName, args, call.SessionID, meta.ProjectID, "agent")
	if err != nil {
		return fail(err)
	}
	message, err := toolText(call.ToolName, args, value, call.SessionID, meta.ProjectID)
	if err != nil {
		return fail(err)
	}
	if len(message) > maxResponseBytes {
		return toolError(fmt.Errorf("%w: response exceeds 2 MiB; narrow filters", errQuota)), nil
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": message}})
	return subprocess.MCPCallResult{Content: content}, err
}
func toolError(err error) subprocess.MCPCallResult {
	raw, _ := json.Marshal([]map[string]string{{"type": "text", "text": err.Error()}})
	return subprocess.MCPCallResult{Content: raw, IsError: true}
}

// Extra agent keys confer no authority: core handlers read only declared fields.
func declaredArguments(name string, args map[string]any) map[string]any {
	out := map[string]any{}
	for _, tool := range agentTools() {
		if tool.Name != name {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		_ = json.Unmarshal(tool.InputSchema, &schema)
		for key := range schema.Properties {
			if v, ok := args[key]; ok {
				out[key] = v
			}
		}
	}
	return out
}
func checkToolArguments(name string, args map[string]any) error {
	for _, tool := range agentTools() {
		if tool.Name != name {
			continue
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type any `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return err
		}
		for _, key := range schema.Required {
			if str(args, key, "") == "" && key != "steps" {
				if name == "todo_update" {
					return updateError("invalid", "id is required", "id")
				}
				return fmt.Errorf("%w: %s required", errInvalid, key)
			}
		}
		for key, def := range schema.Properties {
			value, ok := args[key]
			if !ok || value == nil {
				continue
			}
			if def.Type != nil {
				if _, ok := value.(string); !ok {
					if name == "todo_update" {
						return updateError("invalid", "invalid todo update fields", "")
					}
					if !slices.Contains(schema.Required, key) {
						delete(args, key)
						continue
					}
					return fmt.Errorf("%w: %s must be a string", errInvalid, key)
				}
			}
		}
	}
	return nil
}

type todoPatchError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (e *todoPatchError) Error() string { return e.Message }
func (e *todoPatchError) Unwrap() error {
	switch e.Code {
	case "invalid":
		return errInvalid
	case "not_found":
		return errNotFound
	default:
		return errUnavailable
	}
}
func updateError(code, message, field string) error { return &todoPatchError{code, message, field} }
func todoUpdateFailure(err error) subprocess.MCPCallResult {
	var typed *todoPatchError
	if !errors.As(err, &typed) {
		typed = &todoPatchError{Code: "internal", Message: "internal error"}
		switch {
		case errors.Is(err, errNotFound):
			typed.Code = "not_found"
			typed.Message = "todo not found"
		case errors.Is(err, errUnavailable):
			typed.Code = "unavailable"
			typed.Message = "todo service not available"
		case errors.Is(err, errQuota):
			typed.Code = "invalid"
			typed.Message = "todo update exceeds quota"
		case errors.Is(err, errInvalid):
			typed.Code = "invalid"
			typed.Message = "invalid todo update"
		}
	}
	raw, _ := json.Marshal(typed)
	return toolError(errors.New(string(raw)))
}
func todoToolView(row []pluginapi.DataCell, index map[string]int) record {
	out := project(row, index)
	for _, key := range []string{"labels", "metadata"} {
		out[key] = getCell(row, index, key)
	}
	return out
}
func toolText(name string, args map[string]any, value any, session, pid string) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	switch name {
	case "todo_create", "plan_create":
		row := value.(record)
		kind := "todo"
		if name == "plan_create" {
			kind = "plan"
		}
		return fmt.Sprintf("Created %s %q (id=%s, scope=%s)\n%s", kind, row["title"], row["id"], row["scope"], raw), nil
	case "todo_update":
		return string(raw), nil
	case "plan_update":
		if step := str(args, "step_id", ""); step != "" {
			return fmt.Sprintf("Updated step %s in plan %s", step, str(args, "id", "")), nil
		}
		row := value.(record)
		return fmt.Sprintf("Updated plan %q (id=%s, status=%s)", row["title"], row["id"], row["status"]), nil
	case "plan_step_add":
		result := value.(map[string]any)
		return fmt.Sprintf("Appended %d step(s) to plan %s\n%s", result["appended_count"], result["plan_id"], raw), nil
	case "plan_delete":
		return "Deleted plan " + str(args, "id", ""), nil
	case "todo_list", "plan_list":
		rows := value.([]record)
		kind := "todo"
		if name == "plan_list" {
			kind = "plan"
		}
		var sb strings.Builder
		if len(rows) == 0 {
			fmt.Fprintf(&sb, "No %ss found matching filters.", kind)
		} else {
			fmt.Fprintf(&sb, "Found %d %s(s):\n\n", len(rows), kind)
			for _, row := range rows {
				if kind == "todo" {
					fmt.Fprintf(&sb, "- [%s] %s (id=%s, priority=%s, scope=%s/%s)\n", row["status"], row["title"], row["id"], row["priority"], row["scope"], row["scope_id"])
				} else {
					steps, _ := row["steps"].([]any)
					fmt.Fprintf(&sb, "- [%s] %s (id=%s, scope=%s/%s, steps=%d)\n", row["status"], row["title"], row["id"], row["scope"], row["scope_id"], len(steps))
				}
				if description := str(row, "description", ""); description != "" {
					fmt.Fprintf(&sb, "  %s\n", description)
				}
			}
		}
		if name == "todo_list" && str(args, "scope", "") != "" {
			scope, id, _, err := coordinates(args, session, pid, false, false)
			if err != nil {
				return "", err
			}
			envelope, _ := json.Marshal(map[string]any{"kind": "envelope", "version": 1, "type": "list-card", "data": map[string]any{"title": str(args, "title", "Todos"), "items": []any{}, "data_source": map[string]any{"kind": "todos", "scope": scope, "scope_id": id}}})
			fmt.Fprintf(&sb, "\n<!--ENVELOPE_DATA:%s:ENVELOPE_DATA-->", envelope)
		}
		return sb.String(), nil
	}
	return string(raw), nil
}
func declaration() (manifest.Manifest, error) {
	block := pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{Panels: []pluginapi.Panel{{ID: "work", Title: "Plan", Component: "PlanPanel", Icon: "clipboard-list", DefaultVisible: true, Order: 10}}, HTTPRoutes: []pluginapi.Route{
		{Method: "GET", Path: "todos"}, {Method: "POST", Path: "todos"}, {Method: "GET", Path: "todos/"}, {Method: "PUT", Path: "todos/"}, {Method: "PATCH", Path: "todos/"}, {Method: "DELETE", Path: "todos/"},
		{Method: "GET", Path: "plans"}, {Method: "POST", Path: "plans"}, {Method: "GET", Path: "plans/"}, {Method: "PUT", Path: "plans/"}, {Method: "POST", Path: "plans/"}, {Method: "DELETE", Path: "plans/"}, {Method: "POST", Path: "work/sync"}, {Method: "POST", Path: "work/reorder"},
	}}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	scope, err := json.Marshal(pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryDataExports}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Plan", Version: version, Description: "Durable scoped todos and multi-step plans", License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/plan"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw, Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Import committed todos/plans and resolve session/project coordinates; no transcript content", Metadata: scope}}, Tools: agentTools()}
	if err := pluginapi.ValidateAgentTools(m.Tools); err != nil {
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
		err = subprocess.Serve(&planPlugin{})
	} else {
		err = fmt.Errorf("usage: plan [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
