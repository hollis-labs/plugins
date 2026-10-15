package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func readPlugin(t *testing.T, rows [][]pluginapi.DataCell) *pinsPlugin {
	t.Helper()
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &pinsPlugin{}
	if _, err := p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	source, err := p.ready(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = p.db.transaction(context.Background(), source, func(state *table) (bool, error) {
		state.Snapshot.Rows = append([][]pluginapi.DataCell{}, rows...)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return p
}

func importedPin(id, session, scope, projectID, content string) []pluginapi.DataCell {
	return []pluginapi.DataCell{cell(id), cell(session), cell(scope), cell(projectID), cell(content), cell("original-agent"), cell("2025-01-01"), cell("2025-01-01"), {Kind: "null"}}
}

func readCall(t *testing.T, p *pinsPlugin, tool, session string, arguments map[string]any, target any, limit int) {
	t.Helper()
	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool, SessionID: session, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(result)
	if err != nil || len(wire) > limit {
		t.Fatalf("wire %d exceeds %d: %v", len(wire), limit, err)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err = json.Unmarshal(result.Content, &blocks); err != nil || len(blocks) != 1 || blocks[0].Type != "text" {
		t.Fatal("invalid MCP content", result, err)
	}
	if err = json.Unmarshal([]byte(blocks[0].Text), target); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedInventoryPaginationAndGetImportedContent(t *testing.T) {
	content := strings.Repeat("界\"\n\\\t<>&", 3000) // large, multibyte, heavily escaped imported data
	rows := make([][]pluginapi.DataCell, 135)
	for i := range rows {
		rows[i] = importedPin(fmt.Sprintf("pin-%03d", i), "session-a", "session", "", content)
	}
	rows = append(rows, importedPin("foreign-session", "session-b", "session", "", "foreign"), importedPin("foreign-project", "session-b", "project", "project-b", "foreign"))
	p := readPlugin(t, rows)
	before := readTable(t, p.db.dir, p.source)
	seen := []string{}
	offset := 0
	for {
		var page inventoryPage
		var args map[string]any
		if offset > 0 {
			args = map[string]any{"offset": offset}
		}
		readCall(t, p, "pins_list", "session-a", args, &page, maxInventoryBytes)
		if len(page.Pins) == 0 || len(page.Pins) > 100 || page.NextOffset <= offset {
			t.Fatal("inventory made no progress", page)
		}
		for _, row := range page.Pins {
			if len(row.Preview) > 256 || !utf8.ValidString(row.Preview) || row.ContentBytes != len(content) {
				t.Fatal("unbounded preview or wrong size", row)
			}
			seen = append(seen, row.ID)
		}
		offset = page.NextOffset
		if !page.More {
			break
		}
	}
	if len(seen) != 135 {
		t.Fatal("inventory silently omitted rows", len(seen))
	}
	for i, id := range seen {
		if id != fmt.Sprintf("pin-%03d", i) {
			t.Fatal("equal-time stable order", seen)
		}
	}
	// Reconstruct every byte across chunks; continuation must survive escaping.
	var reconstructed strings.Builder
	offset = 0
	for {
		var page contentPage
		readCall(t, p, "pins_get", "session-a", map[string]any{"id": seen[0], "offset": offset}, &page, maxGetBytes)
		if page.Offset != offset || page.TotalBytes != len(content) || page.NextOffset != offset+len(page.Content) || !utf8.ValidString(page.Content) || len(page.Content) > maxContentChunk {
			t.Fatal("invalid chunk", page)
		}
		reconstructed.WriteString(page.Content)
		if !page.More {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("chunk made no progress")
		}
		offset = page.NextOffset
	}
	if reconstructed.String() != content {
		t.Fatal("chunked get changed imported bytes")
	}
	var initial inventoryPage
	readCall(t, p, "pins_list", "session-a", map[string]any{}, &initial, maxInventoryBytes)
	var empty inventoryPage
	readCall(t, p, "pins_list", "session-a", map[string]any{"offset": 1000}, &empty, maxInventoryBytes)
	if len(empty.Pins) != 0 || empty.More || empty.NextOffset != 135 {
		t.Fatal("end of inventory", empty)
	}
	after := readTable(t, p.db.dir, p.source)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("read changed durable rows")
	}
}

func TestReadArgumentsAndVisibility(t *testing.T) {
	p := readPlugin(t, [][]pluginapi.DataCell{
		importedPin("local", "session-a", "session", "", "界pin"),
		importedPin("project", "session-b", "project", "project-a", "shared"),
		importedPin("foreign", "session-b", "session", "", "hidden"),
		importedPin("other-project", "session-a", "project", "project-b", "hidden"),
	})
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"pins_list", map[string]any{"offset": -1}},
		{"pins_list", map[string]any{"offset": 0.5}},
		{"pins_list", map[string]any{"offset": "0"}},
		{"pins_list", map[string]any{"offset": nil}},
		{"pins_list", map[string]any{"offset": pluginapi.MaxDataExportRows + 1}},
		{"pins_list", map[string]any{"session_id": "session-b"}},
		{"pins_get", nil},
		{"pins_get", map[string]any{"id": "local", "offset": 1}}, // middle of UTF-8 rune
		{"pins_get", map[string]any{"id": "local", "offset": 100}},
		{"pins_get", map[string]any{"id": "local", "offset": -1}},
		{"pins_get", map[string]any{"id": "local", "offset": nil}},
		{"pins_get", map[string]any{"id": "local", "offset": 0.5}},
		{"pins_get", map[string]any{"id": "local", "project_id": "project-b"}},
		{"pins_get", map[string]any{"id": "local", "session_id": "session-b"}},
		{"pins_get", map[string]any{"id": "local", "agent_id": "admin"}},
	} {
		if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tc.tool, SessionID: "session-a", Arguments: tc.args}); err == nil {
			t.Fatal("bad arguments accepted", tc)
		}
	}
	for _, id := range []string{"foreign", "other-project", "absent", "../../file"} {
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "pins_get", SessionID: "session-a", Arguments: map[string]any{"id": id}})
		if !errors.Is(err, errNotFound) {
			t.Fatal("foreign/absent distinguishes existence", id, err)
		}
	}
	var page contentPage
	readCall(t, p, "pins_get", "session-a", map[string]any{"id": "project"}, &page, maxGetBytes)
	if page.Content != "shared" || page.More {
		t.Fatal("same-project visibility", page)
	}
	readCall(t, p, "pins_get", "session-a", map[string]any{"id": "local", "offset": 6}, &page, maxGetBytes)
	if page.Content != "" || page.More || page.NextOffset != 6 {
		t.Fatal("exact content end", page)
	}
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "pins_get", SessionID: "missing", Arguments: map[string]any{"id": "local"}}); err == nil {
		t.Fatal("unavailable session accepted")
	}
}

func TestReadLimitsInvalidContentAndReceiptOwner(t *testing.T) {
	p := readPlugin(t, [][]pluginapi.DataCell{
		importedPin(strings.Repeat("x", 40000), "session-a", "session", "", "pin"),
	})
	for _, tool := range []string{"pins_list", "pins_get"} {
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool, SessionID: "session-a", Arguments: map[string]any{"id": strings.Repeat("x", 40000)}})
		if tool == "pins_list" { // actually test oversize metadata, not argument rejection
			_, err = p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool, SessionID: "session-a"})
		}
		if err == nil {
			t.Fatal("metadata bypassed cap", tool)
		}
	}
	p = readPlugin(t, [][]pluginapi.DataCell{importedPin("bad-content", "session-a", "session", "", "pin\x00")})
	var page inventoryPage
	readCall(t, p, "pins_list", "session-a", nil, &page, maxInventoryBytes)
	if len(page.Pins) != 1 || !strings.Contains(page.Pins[0].Preview, "unavailable") {
		t.Fatal("invalid-content row silently missing", page)
	}
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "pins_get", SessionID: "session-a", Arguments: map[string]any{"id": "bad-content"}}); err == nil {
		t.Fatal("invalid content silently changed")
	}
	foreign := fixtureExport(t, p.db.dir, "workspace-b", [][]pluginapi.DataCell{importedPin("foreign-owner", "session-a", "session", "", "private")})
	if err := p.db.importReceipt(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "pins_get", SessionID: "session-a", Arguments: map[string]any{"id": "foreign-owner"}})
	if !errors.Is(err, errNotFound) {
		t.Fatal("cross-source lookup", err)
	}
}
