package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Independent literal expectation: never call the production notice helper.
func expectedOmissionLine(n int) string {
	return fmt.Sprintf("%d more pins not included; use pins_list, then pins_get (requires an agent tool grant; if unavailable, open the Pins plugin UI).", n)
}

func fetchPinsWire(t *testing.T, p *pinsPlugin, allowance int) subprocess.HTTPResponse {
	t.Helper()
	raw, err := json.Marshal(pluginapi.AlwaysShipRequest{Protocol: 1, SourceID: "pins", SessionID: "session-a", MaxBytes: allowance})
	if err != nil {
		t.Fatal(err)
	}
	response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: pluginapi.AlwaysShipFetchPath, SessionID: "session-a", Body: raw})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAlwaysShipWireAssignedBudget(t *testing.T) {
	p := readPlugin(t, [][]pluginapi.DataCell{
		importedPin("oldest", "session-a", "session", "", strings.Repeat("o", 2500)),
		importedPin("middle", "session-b", "project", "project-a", strings.Repeat("m", 250)),
		importedPin("newest", "session-a", "session", "", strings.Repeat("n", 250)),
	})
	want := "[pinned:project] " + strings.Repeat("m", 250) + "\n[pinned] " + strings.Repeat("n", 250) + "\n1 more pins not included; use pins_list, then pins_get (requires an agent tool grant; if unavailable, open the Pins plugin UI)."
	marker := "3 more pins not included; use pins_list, then pins_get (requires an agent tool grant; if unavailable, open the Pins plugin UI)."
	for _, tc := range []struct {
		allowance int
		want      string
	}{{800, want}, {len(marker), marker}} {
		response := fetchPinsWire(t, p, tc.allowance)
		decoded, err := pluginapi.DecodeAlwaysShipResponse(response.Body, tc.allowance)
		if response.Status != 200 || err != nil || decoded.Body != tc.want {
			t.Fatalf("allowance %d: status %d body %q err %v", tc.allowance, response.Status, decoded.Body, err)
		}
	}
	if response := fetchPinsWire(t, p, len(marker)-1); response.Status < 400 {
		t.Fatal("notice cannot fit but fetch succeeded", response)
	}
}

func TestDemotionNoticeDigitGrowth(t *testing.T) {
	for _, crossing := range []int{10, 100} {
		t.Run(fmt.Sprint(crossing), func(t *testing.T) {
			rows := make([]pin, crossing+1)
			for i := range rows {
				rows[i] = pin{Scope: "session", Content: strings.Repeat("o", 200)}
			}
			rows[crossing-1].Content = "penultimate"
			rows[crossing].Content = "newest"
			two := "[pinned] penultimate\n[pinned] newest\n" + expectedOmissionLine(crossing-1)
			one := "[pinned] newest\n" + expectedOmissionLine(crossing)
			for _, tc := range []struct {
				allowance int
				want      string
			}{{len(two), two}, {len(one), one}, {len(one) - 1, expectedOmissionLine(crossing + 1)}} {
				got, err := renderPins(rows, tc.allowance)
				if err != nil || got != tc.want || len(got) > tc.allowance {
					t.Fatalf("allowance %d: got %q err %v want %q", tc.allowance, got, err, tc.want)
				}
			}
		})
	}
}

func TestInventoryLiteralPageBoundaries(t *testing.T) {
	for _, count := range []int{100, 101, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			rows := make([][]pluginapi.DataCell, count)
			for i := range rows {
				rows[i] = importedPin(fmt.Sprintf("p%03d", i), "session-a", "session", "", "x")
			}
			p := readPlugin(t, rows)
			offset := 0
			for offset < count {
				var page inventoryPage
				readCall(t, p, "pins_list", "session-a", map[string]any{"offset": offset}, &page, 32768)
				wantCount := min(100, count-offset)
				if len(page.Pins) != wantCount || page.NextOffset != offset+wantCount || page.More != (offset+wantCount < count) {
					t.Fatal("literal 100-row boundary", count, offset, page)
				}
				for i, row := range page.Pins {
					if row.ID != fmt.Sprintf("p%03d", offset+i) {
						t.Fatal("page skipped/repeated ID", row.ID)
					}
				}
				offset = page.NextOffset
			}
		})
	}
}

func TestGetLiteral8192ByteChunk(t *testing.T) {
	content := strings.Repeat("a", 20000)
	p := readPlugin(t, [][]pluginapi.DataCell{importedPin("large", "session-a", "session", "", content)})
	var page contentPage
	readCall(t, p, "pins_get", "session-a", map[string]any{"id": "large"}, &page, 16384)
	if len(page.Content) != 8192 || page.Content != content[:8192] || page.NextOffset != 8192 || !page.More || page.TotalBytes != 20000 {
		t.Fatal("literal 8192-byte chunk boundary", len(page.Content), page.NextOffset, page.More)
	}
}

func TestOldContextFetchPathRefused(t *testing.T) {
	p := readPlugin(t, nil)
	response, err := p.HTTPHandle(context.Background(), subprocess.HTTPRequest{Method: "POST", Path: pluginapi.ContextFetchPath, SessionID: "session-a", Body: []byte(`{"protocol":1,"source_id":"pins","session_id":"session-a","max_bytes":6000}`)})
	if err == nil && response.Status < 400 {
		t.Fatal("old compactable fetch path served", response, err)
	}
}

func TestRealFetchCoreCaptureGoldens(t *testing.T) {
	raw, err := os.ReadFile("testdata/core-pins-81f8d3f2.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []struct {
			Name string
			Rows []struct {
				ID      string `json:"id"`
				Session string `json:"session_id"`
				Scope   string `json:"scope"`
				Project string `json:"project_id"`
				Content string `json:"content"`
				Agent   string `json:"agent_id"`
				Created string `json:"created_at"`
			}
			Core string
		}
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	manifest, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	block, err := pluginapi.DecodeBlock(manifest.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	mapID := func(id string) string {
		switch id {
		case "sa":
			return "session-a"
		case "sb":
			return "session-b"
		case "pa":
			return "project-a"
		case "pb":
			return "project-b"
		}
		return id
	}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			rows := make([][]pluginapi.DataCell, 0, len(fixture.Rows))
			for _, row := range fixture.Rows {
				cells := importedPin(row.ID, mapID(row.Session), row.Scope, mapID(row.Project), row.Content)
				cells[5], cells[6], cells[7] = cell(row.Agent), cell(row.Created), cell(row.Created)
				rows = append(rows, cells)
			}
			response := fetchPinsWire(t, readPlugin(t, rows), 6000)
			body, decodeErr := pluginapi.DecodeAlwaysShipResponse(response.Body, 6000)
			if response.Status != 200 || decodeErr != nil {
				t.Fatal(response, decodeErr)
			}
			got := body.Body
			if got != "" {
				got = "## " + block.Registers.AlwaysShipSources[0].Title + "\n" + got
			}
			if got != fixture.Core {
				t.Fatalf("real fetch differs from core capture: got %q want %q", got, fixture.Core)
			}
		})
	}
}
