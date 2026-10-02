package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	pluginID = "nanite.documents"
	version  = "0.1.0"
)

var _ subprocess.MCPHandler = (*documentsPlugin)(nil)
var _ subprocess.HTTPHandler = (*documentsPlugin)(nil)

type documentsPlugin struct {
	mu     sync.Mutex
	client *pluginapi.QueryClient
	db     database
	source string
	closed bool
}

func (p *documentsPlugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID {
		return subprocess.InitResult{}, fmt.Errorf("documents: query grant owner differs")
	}
	client, err := pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if params.DataDir == "" {
		return subprocess.InitResult{}, fmt.Errorf("documents: DataDir required")
	}
	if err = os.MkdirAll(params.DataDir, 0700); err != nil {
		return subprocess.InitResult{}, err
	}
	p.mu.Lock()
	p.client = client
	p.db = database{dir: params.DataDir}
	p.closed = false
	p.source = ""
	p.mu.Unlock()
	return subprocess.InitResult{ID: pluginID, Name: "Documents", Version: version, Description: "Durable session text documents", Protocol: subprocess.ProtocolVersion}, nil
}
func (*documentsPlugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *documentsPlugin) Unload(context.Context) error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nil
}
func (p *documentsPlugin) ready(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client == nil {
		return "", fmt.Errorf("documents unavailable")
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
		if candidate.Feature != "documents" {
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
func (p *documentsPlugin) session(ctx context.Context, id string) (pluginapi.QuerySession, error) {
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

const maxContentBytes = 512 << 10
const maxSummaryBytes = 8192
const maxResponseBytes = 2 << 20

type createRequest struct {
	Name        string `json:"name"`
	MimeType    string `json:"mime_type,omitempty"`
	Content     string `json:"content"`
	Summary     string `json:"summary,omitempty"`
	Included    bool   `json:"included,omitempty"`
	FullContent bool   `json:"full_content,omitempty"`
}

// Require content even for an intentionally empty uploaded text file.
func (r *createRequest) UnmarshalJSON(raw []byte) error {
	type plain createRequest
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if v, ok := fields["content"]; !ok || string(v) == "null" {
		return fmt.Errorf("content required")
	}
	var decoded plain
	if err := decodeRequest(raw, &decoded); err != nil {
		return err
	}
	*r = createRequest(decoded)
	return nil
}

type patchRequest struct {
	Included    *bool   `json:"included,omitempty"`
	FullContent *bool   `json:"full_content,omitempty"`
	Summary     *string `json:"summary,omitempty"`
}

func validText(s string, maxBytes int) bool {
	return utf8.ValidString(s) && len(s) <= maxBytes && !strings.ContainsRune(s, 0)
}
func validateCreate(req createRequest) error {
	if strings.TrimSpace(req.Name) == "" || !validText(req.Name, 512) || !validText(req.MimeType, 256) || !validText(req.Content, maxContentBytes) || !validText(req.Summary, maxSummaryBytes) {
		return fmt.Errorf("invalid document: name required; UTF-8 without NUL; name 512, MIME 256, content 524288, summary 8192 bytes maximum")
	}
	return nil
}
func validatePatch(req patchRequest) error {
	if req.Included == nil && req.FullContent == nil && req.Summary == nil {
		return fmt.Errorf("empty document patch")
	}
	if req.Summary != nil && !validText(*req.Summary, maxSummaryBytes) {
		return fmt.Errorf("invalid summary")
	}
	return nil
}

// Requests carry inline text and can exceed the manifest codec's 1 MiB cap.
// These flat request objects use the same strict ambiguity policy, bounded by
// the HTTP contract: duplicate/unknown fields and trailing JSON are refused.
func decodeRequest(raw []byte, value any) error {
	if len(raw) > pluginapi.MaxHTTPBody || !utf8.Valid(raw) {
		return fmt.Errorf("invalid or oversized UTF-8 request")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("object required")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{}
	requestType := reflect.TypeOf(value).Elem()
	for i := 0; i < requestType.NumField(); i++ {
		allowed[strings.Split(requestType.Field(i).Tag.Get("json"), ",")[0]] = true
	}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] || !allowed[key] {
			return fmt.Errorf("duplicate or unknown request field")
		}
		seen[key] = true
		var field json.RawMessage
		if err = decoder.Decode(&field); err != nil {
			return err
		}
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return fmt.Errorf("null request field")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing request data")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
func decodeArgs(args map[string]any, value any) error {
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return decodeRequest(raw, value)
}
func (p *documentsPlugin) create(ctx context.Context, session string, req createRequest) (document, error) {
	if err := validateCreate(req); err != nil {
		return document{}, err
	}
	source, err := p.ready(ctx)
	if err != nil {
		return document{}, err
	}
	if _, err = p.session(ctx, session); err != nil {
		return document{}, err
	}
	if req.MimeType == "" {
		req.MimeType = "text/plain"
	}
	return p.db.create(ctx, source, document{SessionID: session, Name: req.Name, MimeType: req.MimeType, Content: req.Content, SizeBytes: int64(len(req.Content)), Summary: req.Summary, Included: req.Included, FullContent: req.FullContent})
}
func (p *documentsPlugin) MCPCallTool(ctx context.Context, call subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	source, err := p.ready(ctx)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if _, err = p.session(ctx, call.SessionID); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	var value any
	switch call.ToolName {
	case "documents_create":
		var req createRequest
		if err = decodeArgs(call.Arguments, &req); err == nil {
			var created document
			created, err = p.create(ctx, call.SessionID, req)
			created.Content = ""
			value = created
		}
	case "documents_list":
		var args struct {
			Offset int `json:"offset,omitempty"`
		}
		if err = decodeArgs(call.Arguments, &args); err == nil {
			value, err = p.db.page(ctx, source, call.SessionID, args.Offset)
		}
	case "documents_get":
		var args struct {
			ID     string `json:"id"`
			Offset int    `json:"offset,omitempty"`
			Limit  int    `json:"limit,omitempty"`
		}
		if err = decodeArgs(call.Arguments, &args); err == nil {
			value, err = p.db.read(ctx, source, call.SessionID, args.ID, args.Offset, args.Limit)
		}
	case "documents_update":
		var args struct {
			ID          string  `json:"id"`
			Included    *bool   `json:"included,omitempty"`
			FullContent *bool   `json:"full_content,omitempty"`
			Summary     *string `json:"summary,omitempty"`
		}
		if err = decodeArgs(call.Arguments, &args); err == nil {
			err = p.db.change(ctx, source, call.SessionID, args.ID, patchRequest{args.Included, args.FullContent, args.Summary}, false)
			value = map[string]string{"id": args.ID, "status": "updated"}
		}
	case "documents_delete":
		var args struct {
			ID string `json:"id"`
		}
		if err = decodeArgs(call.Arguments, &args); err == nil {
			err = p.db.change(ctx, source, call.SessionID, args.ID, patchRequest{}, true)
			value = map[string]string{"id": args.ID, "status": "deleted"}
		}
	default:
		err = fmt.Errorf("unknown document tool")
	}
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if len(raw) > maxResponseBytes {
		return subprocess.MCPCallResult{}, fmt.Errorf("response exceeds limit")
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": string(raw)}})
	return subprocess.MCPCallResult{Content: content}, err
}
func declaration() (manifest.Manifest, error) {
	block := pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Slots:          []pluginapi.Slot{{ID: "documents", Slot: pluginapi.SlotWorkingDrawer, Component: "DocumentsTab", Title: "Documents", Icon: "FileText", Priority: 40}},
		ContextSources: []pluginapi.ContextSource{{ID: "documents"}},
		HTTPRoutes:     []pluginapi.Route{{Method: "GET", Path: "documents"}, {Method: "POST", Path: "documents"}, {Method: "GET", Path: "documents/"}, {Method: "PATCH", Path: "documents/"}, {Method: "DELETE", Path: "documents/"}}}}
	raw, err := pluginapi.EncodeBlock(block)
	if err != nil {
		return manifest.Manifest{}, err
	}
	query, err := json.Marshal(pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QuerySessions, pluginapi.QueryDataExports}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	scope, err := json.Marshal(pluginapi.ContextScope{SourceIDs: []string{"documents"}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	tools := []manifest.Tool{
		{Name: "documents_create", Effect: "write", Description: "Create a text document in the calling session; excluded from context by default", InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","maxLength":512},"mime_type":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":524288},"summary":{"type":"string","maxLength":8192},"included":{"type":"boolean"},"full_content":{"type":"boolean"}},"required":["name","content"],"additionalProperties":false}`)},
		{Name: "documents_list", Effect: "read", Description: "List session document metadata, 100 per page, without content", InputSchema: json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0,"maximum":1000000}},"additionalProperties":false}`)},
		{Name: "documents_get", Effect: "read", Description: "Read a session document in UTF-8 byte chunks (default and maximum 65536 bytes); next_offset continues", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":65536}},"required":["id"],"additionalProperties":false}`)},
		{Name: "documents_update", Effect: "write", Description: "Update inclusion, full-content mode or summary; omitted fields stay unchanged", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"},"included":{"type":"boolean"},"full_content":{"type":"boolean"},"summary":{"type":"string","maxLength":8192}},"required":["id"],"additionalProperties":false}`)},
		{Name: "documents_delete", Effect: "write", Description: "Delete a document from the calling session", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)}}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Documents", Description: "Session text documents with bounded full-content or pointer context", Version: version, License: "Apache-2.0", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/documents"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: raw, Tools: tools,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Import committed documents and resolve session metadata; no message content", Metadata: query}, {Name: pluginapi.CapabilityContextSource, Reason: "Include selected documents within the session context budget", Metadata: scope}}}
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
		err = subprocess.Serve(&documentsPlugin{})
	} else {
		err = fmt.Errorf("usage: documents [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
