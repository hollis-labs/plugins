package sprite

import (
	"encoding/json"
	"os"
	"testing"
)

// The native port must reproduce the Hadron workflow byte for byte; golden.json
// is that workflow's own output (see capture_test.go).
func TestMatchesTheWorkflow(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		testCase
		SVG    string          `json:"svg"`
		Sprite json.RawMessage `json:"sprite"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	if len(golden) != len(cases()) {
		t.Fatalf("golden has %d cases, cases() has %d: refresh it", len(golden), len(cases()))
	}
	for _, g := range golden {
		s := Draw(g.ID, g.Fill, g.Hue)
		if got := SVG(s, g.Accent); got != g.SVG {
			t.Errorf("%+v svg differs:\n got  %s\n want %s", g.testCase, got, g.SVG)
		}
		var want, got any
		_ = json.Unmarshal(g.Sprite, &want)
		_ = json.Unmarshal(s.JSON(), &got)
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if string(wb) != string(gb) {
			t.Errorf("%+v sprite differs:\n got  %s\n want %s", g.testCase, gb, wb)
		}
	}
}
