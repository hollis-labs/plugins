// Bookmarks stores plugin-owned data and retains references to core messages.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	pluginID = "nanite.bookmarks"
	version  = "0.1.0"
)

type bookmarks struct {
	client *pluginapi.QueryClient
	db     database
	mu     sync.Mutex
	source string
	closed bool
}

func (p *bookmarks) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if grant.PluginID != pluginID {
		return subprocess.InitResult{}, fmt.Errorf("bookmarks: query grant owner differs")
	}
	p.client, err = pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		return subprocess.InitResult{}, err
	}
	if params.DataDir == "" {
		return subprocess.InitResult{}, fmt.Errorf("bookmarks: DataDir required")
	}
	if err = os.MkdirAll(params.DataDir, 0700); err != nil {
		return subprocess.InitResult{}, err
	}
	p.db = database{dir: params.DataDir}
	return subprocess.InitResult{ID: pluginID, Name: "Bookmarks", Description: "Session message bookmarks", Version: version, Protocol: subprocess.ProtocolVersion}, nil
}
func (p *bookmarks) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}
func (p *bookmarks) Unload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// Orphan exports are ignored. Plugin writes wait for a committed core receipt.
func (p *bookmarks) ready(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.client == nil {
		return "", fmt.Errorf("bookmarks: plugin unavailable")
	}
	if p.source != "" {
		return p.source, nil
	}
	receipts, err := p.client.ExportReceipts(ctx, 100)
	if err != nil {
		return "", err
	}
	if receipts.More {
		return "", fmt.Errorf("bookmarks: incomplete receipt list")
	}
	var receipt *pluginapi.DataExportReceipt
	for _, candidate := range receipts.Exports {
		if candidate.Feature != "bookmarks" {
			continue
		}
		if receipt != nil {
			return "", fmt.Errorf("bookmarks: ambiguous workspace receipt")
		}
		r := candidate
		receipt = &r
	}
	if receipt == nil {
		return "", fmt.Errorf("bookmarks: waiting for committed core import")
	}
	if err = p.db.importReceipt(ctx, *receipt); err != nil {
		return "", err
	}
	p.source = receipt.SourceID
	return p.source, nil
}
func validateNote(note string) error {
	if len(note) > 8192 || strings.ContainsRune(note, 0) {
		return fmt.Errorf("bookmarks: note exceeds limit or contains NUL")
	}
	return nil
}
func (p *bookmarks) save(ctx context.Context, reference pluginapi.CoreReference, note string, toggle bool) (bookmark, bool, error) {
	if err := validateNote(note); err != nil {
		return bookmark{}, false, err
	}
	source, err := p.ready(ctx)
	if err != nil {
		return bookmark{}, false, err
	}
	if _, err = p.client.ResolveReference(ctx, reference); err != nil {
		return bookmark{}, false, err
	}
	return p.db.create(ctx, source, reference, note, toggle)
}

type bookmarkPage struct {
	Bookmarks  []bookmark `json:"bookmarks"`
	More       bool       `json:"more"`
	NextOffset int        `json:"next_offset"`
}

func pageRows(rows []bookmark, offset int) (bookmarkPage, error) {
	if offset < 0 || offset > pluginapi.MaxDataExportRows {
		return bookmarkPage{}, fmt.Errorf("bookmarks: invalid offset")
	}
	start := min(offset, len(rows))
	end := min(start+100, len(rows))
	return bookmarkPage{Bookmarks: rows[start:end], More: end < len(rows), NextOffset: end}, nil
}
func offsetArgument(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	number, ok := value.(float64)
	if !ok || number < 0 || number > pluginapi.MaxDataExportRows || number != float64(int(number)) {
		return 0, fmt.Errorf("bookmarks: invalid offset")
	}
	return int(number), nil
}
func queryOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	return strconv.Atoi(raw)
}
func (p *bookmarks) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	var value any
	var err error
	switch req.ToolName {
	case "bookmarks_list":
		source, readyErr := p.ready(ctx)
		if readyErr != nil {
			return subprocess.MCPCallResult{}, readyErr
		}
		if err = (pluginapi.CoreReference{SessionID: req.SessionID, MessageID: "validation"}).Validate(); err == nil {
			var rows []bookmark
			rows, err = p.db.list(ctx, source, req.SessionID)
			if err == nil {
				var offset int
				offset, err = offsetArgument(req.Arguments["offset"])
				if err == nil {
					value, err = pageRows(rows, offset)
				}
			}
		}
	case "bookmarks_save":
		message, ok := req.Arguments["message_id"].(string)
		if !ok {
			return subprocess.MCPCallResult{}, fmt.Errorf("bookmarks: message_id required")
		}
		note := ""
		if raw, exists := req.Arguments["note"]; exists {
			var valid bool
			note, valid = raw.(string)
			if !valid {
				return subprocess.MCPCallResult{}, fmt.Errorf("bookmarks: note must be text")
			}
		}
		value, _, err = p.save(ctx, pluginapi.CoreReference{SessionID: req.SessionID, MessageID: message}, note, false)
	case "bookmarks_delete":
		id, ok := req.Arguments["id"].(string)
		if !ok {
			return subprocess.MCPCallResult{}, fmt.Errorf("bookmarks: id required")
		}
		source, readyErr := p.ready(ctx)
		if readyErr != nil {
			return subprocess.MCPCallResult{}, readyErr
		}
		rows, listErr := p.db.list(ctx, source, req.SessionID)
		if listErr != nil {
			return subprocess.MCPCallResult{}, listErr
		}
		owned := false
		for _, row := range rows {
			if row.ID == id {
				owned = true
			}
		}
		if !owned {
			return subprocess.MCPCallResult{}, errNotFound
		}
		err = p.db.change(ctx, source, id, nil)
		value = map[string]bool{"deleted": err == nil}
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
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
func (p *bookmarks) latest(ctx context.Context, session string) (pluginapi.CoreReference, error) {
	if _, err := p.ready(ctx); err != nil {
		return pluginapi.CoreReference{}, err
	}
	response, err := p.client.Query(ctx, pluginapi.QueryRequest{Resource: pluginapi.QueryMessageReferences, SessionID: session, Limit: 100})
	if err != nil {
		return pluginapi.CoreReference{}, err
	}
	var data pluginapi.QueryMessageReferencesData
	if err = manifest.DecodeExtension(response.Data, &data); err != nil {
		return pluginapi.CoreReference{}, err
	}
	for _, row := range data.References {
		if row.Role == "assistant" && row.SessionID == session {
			return pluginapi.CoreReference{SessionID: session, MessageID: row.MessageID}, nil
		}
	}
	return pluginapi.CoreReference{}, fmt.Errorf("bookmarks: no assistant reply among the latest 100 messages")
}
func (p *bookmarks) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
	if req.Name != "bookmark" {
		return subprocess.CommandResult{}, fmt.Errorf("unknown command")
	}
	args := strings.TrimSpace(req.Args)
	message, note, _ := strings.Cut(args, " ")
	if message == "" {
		reference, err := p.latest(ctx, req.SessionID)
		if err != nil {
			return subprocess.CommandResult{}, err
		}
		message = reference.MessageID
	}
	b, _, err := p.save(ctx, pluginapi.CoreReference{SessionID: req.SessionID, MessageID: message}, strings.TrimSpace(note), false)
	if err != nil {
		return subprocess.CommandResult{}, err
	}
	return subprocess.CommandResult{Action: "noop", Content: "Bookmarked " + b.MessageID}, nil
}
func declaration() (manifest.Manifest, error) {
	block, err := pluginapi.EncodeBlock(pluginapi.Block{UI: pluginapi.UI{Bundle: "ui/index.js"}, Registers: pluginapi.Registrations{
		Panels:     []pluginapi.Panel{{ID: "bookmark-list", Title: "Bookmarks", Component: "BookmarksWidget", Icon: "Bookmark", DefaultVisible: true, Order: 20}},
		Commands:   []pluginapi.Command{{Name: "bookmark", Description: "Bookmark the latest reply, or /bookmark <message ID> [title]"}},
		HTTPRoutes: []pluginapi.Route{{Method: "GET", Path: "bookmarks"}, {Method: "POST", Path: "bookmarks"}, {Method: "PATCH", Path: "bookmarks/"}, {Method: "DELETE", Path: "bookmarks/"}, {Method: "POST", Path: "toggle"}, {Method: "POST", Path: "latest"}},
	}})
	if err != nil {
		return manifest.Manifest{}, err
	}
	scope, err := json.Marshal(pluginapi.QueryScope{Resources: []pluginapi.QueryResource{pluginapi.QueryMessageReferences, pluginapi.QueryDataExports}, AllSessions: true})
	if err != nil {
		return manifest.Manifest{}, err
	}
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: pluginID, Name: "Bookmarks", Description: "Bookmark session messages and preserve imported notes and tags", Version: version, License: "MIT", Repository: "https://github.com/hollis-labs/nanite-plugins", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime, Entrypoint: manifest.Entrypoint{Command: "bin/bookmarks"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: pluginapi.Version}}, Nanite: block,
		Capabilities: []subprocess.CapabilityRequest{{Name: pluginapi.CapabilityReadOnlyQuery, Reason: "Verify message/session references and import only committed bookmarks exports; no message content access", Metadata: scope}},
		Tools: []manifest.Tool{
			{Name: "bookmarks_list", Description: "List bookmarks in the calling session, 100 per page", Effect: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"offset":{"type":"integer","minimum":0,"maximum":1000000}},"additionalProperties":false}`)},
			{Name: "bookmarks_save", Description: "Bookmark a message in the calling session with an optional title", Effect: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{"message_id":{"type":"string"},"note":{"type":"string","maxLength":8192}},"required":["message_id"],"additionalProperties":false}`)},
			{Name: "bookmarks_delete", Description: "Delete a bookmark in the calling session", Effect: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)},
		}}
	if err = m.Validate(); err != nil {
		return manifest.Manifest{}, err
	}
	return m, nil
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
		err = subprocess.Serve(&bookmarks{})
	} else {
		err = fmt.Errorf("usage: bookmarks [--manifest]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
