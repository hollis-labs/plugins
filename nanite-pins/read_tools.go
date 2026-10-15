package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const (
	maxInventoryBytes = 32 << 10
	maxGetBytes       = 16 << 10
	maxContentChunk   = 8 << 10
	maxPreviewBytes   = 256
)

type inventoryPin struct {
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	Preview      string `json:"preview"`
	ContentBytes int    `json:"content_bytes"`
}

type inventoryPage struct {
	Pins       []inventoryPin `json:"pins"`
	More       bool           `json:"more"`
	NextOffset int            `json:"next_offset"`
}

type contentPage struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	TotalBytes int    `json:"total_bytes"`
	More       bool   `json:"more"`
	NextOffset int    `json:"next_offset"`
}

// Bound the complete SDK MCP result, including the second layer of escaping
// around the text payload. Counting just decoded content is insufficient.
func boundedToolResult(value any, limit int) (subprocess.MCPCallResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": string(raw)}})
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	result := subprocess.MCPCallResult{Content: content}
	wire, err := json.Marshal(result)
	if err != nil || len(wire) > limit {
		return subprocess.MCPCallResult{}, fmt.Errorf("pin result exceeds byte limit")
	}
	return result, nil
}

func decodeReadArguments(arguments map[string]any, value any) error {
	if arguments == nil {
		arguments = map[string]any{}
	}
	for _, argument := range arguments {
		if argument == nil {
			return fmt.Errorf("null pin argument")
		}
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return err
	}
	return manifest.DecodeExtension(raw, value)
}

func utf8Prefix(text string, size int) string {
	end := min(size, len(text))
	for end > 0 && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func (p *pinsPlugin) listInventory(ctx context.Context, source, session, projectID string, arguments map[string]any) (subprocess.MCPCallResult, error) {
	var args struct {
		Offset int `json:"offset,omitempty"`
	}
	if err := decodeReadArguments(arguments, &args); err != nil || args.Offset < 0 || args.Offset > pluginapi.MaxDataExportRows {
		return subprocess.MCPCallResult{}, fmt.Errorf("invalid list offset")
	}
	rows, err := p.db.list(ctx, source, session, projectID)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	start := min(args.Offset, len(rows))
	page := inventoryPage{Pins: []inventoryPin{}, More: start < len(rows), NextOffset: start}
	for end := start; end < min(start+100, len(rows)); end++ {
		row := rows[end]
		if !utf8.ValidString(row.ID) || strings.ContainsRune(row.ID, 0) {
			return subprocess.MCPCallResult{}, fmt.Errorf("pin ID cannot be rendered")
		}
		preview := "[content unavailable: invalid UTF-8 or NUL]"
		if utf8.ValidString(row.Content) && !strings.ContainsRune(row.Content, 0) {
			preview = utf8Prefix(row.Content, maxPreviewBytes)
		}
		page.Pins = append(page.Pins, inventoryPin{ID: row.ID, Scope: row.Scope, Preview: preview, ContentBytes: len(row.Content)})
		page.NextOffset = end + 1
		page.More = page.NextOffset < len(rows)
		if _, err = boundedToolResult(page, maxInventoryBytes); err != nil {
			page.Pins = page.Pins[:len(page.Pins)-1]
			page.NextOffset = end
			page.More = true
			if len(page.Pins) == 0 {
				return subprocess.MCPCallResult{}, fmt.Errorf("pin inventory metadata exceeds byte limit")
			}
			break
		}
	}
	return boundedToolResult(page, maxInventoryBytes)
}

func (p *pinsPlugin) getContent(ctx context.Context, source, session, projectID string, arguments map[string]any) (subprocess.MCPCallResult, error) {
	var args struct {
		ID     string `json:"id"`
		Offset int    `json:"offset,omitempty"`
	}
	if err := decodeReadArguments(arguments, &args); err != nil || args.ID == "" || !utf8.ValidString(args.ID) || strings.ContainsRune(args.ID, 0) || args.Offset < 0 {
		return subprocess.MCPCallResult{}, fmt.Errorf("id and valid content offset required")
	}
	rows, err := p.db.list(ctx, source, session, projectID)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	for _, row := range rows {
		if row.ID != args.ID {
			continue
		}
		if !utf8.ValidString(row.Content) || strings.ContainsRune(row.Content, 0) {
			return subprocess.MCPCallResult{}, fmt.Errorf("pin content cannot be rendered")
		}
		if args.Offset > len(row.Content) || (args.Offset < len(row.Content) && !utf8.RuneStart(row.Content[args.Offset])) {
			return subprocess.MCPCallResult{}, fmt.Errorf("invalid content offset")
		}
		page := contentPage{ID: row.ID, Offset: args.Offset, TotalBytes: len(row.Content)}
		// Binary search for the largest UTF-8 chunk whose complete MCP encoding
		// fits. Imported content can exceed the creation limit, or escape heavily.
		low, high := 0, min(maxContentChunk, len(row.Content)-args.Offset)
		var result subprocess.MCPCallResult
		var accepted contentPage
		for low <= high {
			mid := low + (high-low)/2
			page.Content = utf8Prefix(row.Content[args.Offset:], mid)
			page.NextOffset = args.Offset + len(page.Content)
			page.More = page.NextOffset < len(row.Content)
			trial, encodeErr := boundedToolResult(page, maxGetBytes)
			if encodeErr != nil {
				high = mid - 1
			} else {
				result = trial
				accepted = page
				low = mid + 1
			}
		}
		if len(result.Content) == 0 || (high == 0 && args.Offset < len(row.Content)) {
			return subprocess.MCPCallResult{}, fmt.Errorf("pin metadata exceeds byte limit")
		}
		// A large ID may leave too little room even for a single multibyte rune.
		if accepted.More && accepted.NextOffset == args.Offset {
			return subprocess.MCPCallResult{}, fmt.Errorf("pin metadata leaves no content space")
		}
		return result, nil
	}
	return subprocess.MCPCallResult{}, errNotFound
}
