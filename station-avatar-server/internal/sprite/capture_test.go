//go:build hadron_golden

package sprite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
)

// TestCaptureGolden regenerates testdata/golden.json from a live hadrond running
// the workflow. Run it only to refresh the reference, never to make the native
// port pass:
//
//	AVATAR_WORKFLOW=... go test -tags hadron_golden -run CaptureGolden ./internal/sprite
func TestCaptureGolden(t *testing.T) {
	home, _ := os.UserHomeDir()
	c := &hadron.Client{BaseURL: hadron.DefaultBaseURL, Token: hadron.TokenFile(filepath.Join(home, ".hadron", "operator.token")),
		Locator: os.Getenv("AVATAR_WORKFLOW"), Timeout: 30 * time.Second}
	cs := cases()
	type golden struct {
		testCase
		SVG    string          `json:"svg"`
		Sprite json.RawMessage `json:"sprite"`
	}
	out := make([]golden, len(cs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, tc := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := avatar.Request{ID: tc.ID, Knobs: avatar.Knobs{Fill: tc.Fill, Hue: tc.Hue}, Accent: tc.Accent}
			res, err := c.Run(context.Background(), r.RunID(), map[string]any{"id": tc.ID, "fill": tc.Fill, "hue": tc.Hue, "accent": tc.Accent})
			if err != nil {
				t.Errorf("%+v: %v", tc, err)
				return
			}
			g := golden{testCase: tc, Sprite: res["sprite"]}
			_ = json.Unmarshal(res["svg"], &g.SVG)
			out[i] = g
		}()
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	b, _ := json.MarshalIndent(out, "", " ")
	if err := os.WriteFile("testdata/golden.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
}
