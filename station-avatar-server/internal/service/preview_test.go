package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

func TestPreviewWritesNothingAndTakesNoSlot(t *testing.T) {
	f := setup(t)
	f.s.Previewer = f.svg
	fill := 30
	p, err := f.s.Preview(context.Background(), PreviewRequest{Seed: "agt-new", Fill: &fill})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.SVG, "<svg") || p.Fill != 30 || p.Palette.BG == "" || p.Palette.Slot == "" {
		t.Fatalf("preview: %+v", p)
	}
	if f.svg.last.Knobs.Fill != 30 || f.svg.last.ID != "agt-new" {
		t.Fatalf("engine saw %+v", f.svg.last)
	}
	if png, err := p.PNG(64); err != nil || len(png) == 0 {
		t.Fatalf("png: %v", err)
	}
	entries, _ := os.ReadDir(f.dir)
	if len(entries) != 0 {
		t.Fatalf("preview wrote files: %v", entries)
	}
	if _, ok, _ := f.s.Registry.Peek("agt-new"); ok {
		t.Fatal("preview took a palette slot")
	}
}

func TestPreviewShowsTheColoursGenerateWillUse(t *testing.T) {
	f := setup(t)
	f.s.Previewer = f.svg
	gen(t, f, Request{URN: "agt-a", Mode: ModeSVG})
	got, err := f.s.Preview(context.Background(), PreviewRequest{Seed: "agt-a"})
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := f.s.Store.ReadSidecar("agt-a")
	if got.Palette.Accent != sc.Avatar.Palette.Accent || got.Palette.BG != sc.Avatar.Palette.BG {
		t.Fatalf("preview colours %+v differ from stored %+v", got.Palette, sc.Avatar.Palette)
	}
}

func TestPreviewValidates(t *testing.T) {
	f := setup(t)
	f.s.Previewer = f.svg
	bad := 101
	for name, r := range map[string]PreviewRequest{
		"no seed":      {},
		"fill":         {Seed: "a", Fill: &bad},
		"half a pair":  {Seed: "a", Accent: "#ff0000"},
		"both":         {Seed: "a", Palette: "violet", Accent: "#ff0000", BG: "#000000"},
		"unknown slot": {Seed: "a", Palette: "nope"},
	} {
		var fe *avatar.FieldError
		if _, err := f.s.Preview(context.Background(), r); !errors.As(err, &fe) {
			t.Errorf("%s: want a FieldError, got %v", name, err)
		}
	}
}

func TestDeleteArchivesAndRegenerates(t *testing.T) {
	f := setup(t)
	gen(t, f, Request{URN: "agt-d", Mode: ModeSVG})
	if _, err := f.s.Delete(context.Background(), "agt-d"); err != nil {
		t.Fatal(err)
	}
	if f.s.Store.Exists("agt-d") {
		t.Fatal("avatar still in service")
	}
	archived, _ := filepath.Glob(filepath.Join(f.dir, "agt-d.*Z.png"))
	if len(archived) != 1 {
		t.Fatalf("want the old face kept, got %v", archived)
	}
	if resp := gen(t, f, Request{URN: "agt-d", Mode: ModeSVG}); resp.Existed {
		t.Fatal("a deleted avatar should be generated afresh")
	}
	if _, err := f.s.Delete(context.Background(), "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestResponseCarriesURLsWhenConfigured(t *testing.T) {
	f := setup(t)
	if resp := gen(t, f, Request{URN: "agt-u", Mode: ModeSVG}); resp.URLs != nil {
		t.Fatalf("urls without a public url: %v", resp.URLs)
	}
	f.s.PublicURL = "https://avatars.example/avatar-http"
	resp := gen(t, f, Request{URN: "agt-u", Mode: ModeSVG})
	if resp.URLs["png"] != "https://avatars.example/avatar-http/avatars/agt-u.png" || resp.URLs["svg"] == "" || resp.URLs["png8x"] == "" {
		t.Fatalf("urls: %v", resp.URLs)
	}
}
