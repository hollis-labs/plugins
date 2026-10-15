package render

import (
	"bytes"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// A 2-colour sprite on the real workflow's output shape: one unit rect per lit pixel.
const sprite = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" width="64" height="64" shape-rendering="crispEdges">` +
	`<rect x="1" y="0" width="1" height="1" fill="#ff6b5b"/>` +
	`<rect x="2" y="5" width="1" height="1" fill="hsl(200 65% 38%)"/>` +
	`<rect x="3" y="2" width="1" height="1" fill="#a7f3d0"/></svg>`

func decode(t *testing.T, b []byte) (w, h int, at func(x, y int) color.NRGBA) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy(), func(x, y int) color.NRGBA {
		return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	}
}

func TestPNGScalesByAnIntegerWithNoSmoothing(t *testing.T) {
	b, err := PNG(sprite, 64, "")
	if err != nil {
		t.Fatal(err)
	}
	w, h, at := decode(t, b)
	if w != 64 || h != 64 {
		t.Fatalf("size %dx%d", w, h)
	}
	// Cell (1,0) covers x 8..15, y 0..7 at scale 8: every pixel is the exact colour.
	for _, p := range [][2]int{{8, 0}, {15, 7}, {11, 3}} {
		if c := at(p[0], p[1]); c != (color.NRGBA{0xff, 0x6b, 0x5b, 255}) {
			t.Errorf("pixel %v = %v", p, c)
		}
	}
	if c := at(7, 0); c.A != 0 {
		t.Errorf("outside the cell must be transparent, got %v", c)
	}
}

func TestPNGIsByteStable(t *testing.T) {
	a, _ := PNG(sprite, 512, "#1f2a44")
	b, _ := PNG(sprite, 512, "#1f2a44")
	if !bytes.Equal(a, b) {
		t.Fatal("the same SVG must give the same PNG bytes")
	}
}

func TestBackgroundIsFlatAndOpaque(t *testing.T) {
	b, err := PNG(sprite, 64, "#1F2A44")
	if err != nil {
		t.Fatal(err)
	}
	_, _, at := decode(t, b)
	if c := at(0, 63); c != (color.NRGBA{0x1f, 0x2a, 0x44, 255}) {
		t.Errorf("background pixel = %v", c)
	}
	if c := at(8, 0); c != (color.NRGBA{0xff, 0x6b, 0x5b, 255}) {
		t.Errorf("sprite must sit on top of the background, got %v", c)
	}
}

func TestHSLMatchesTheBrowser(t *testing.T) {
	// Expected values are Chrome's getComputedStyle for the same strings, not
	// hand-computed: the SVG is what a browser draws, so the PNG must agree.
	for in, want := range map[string]color.NRGBA{
		"hsl(323 65% 58%)": {218, 78, 164, 255},
		"hsl(200 65% 38%)": {34, 118, 160, 255},
		"hsl(0 100% 50%)":  {255, 0, 0, 255},
		"hsl(323 65% 38%)": {160, 34, 112, 255},
		"hsl(109 65% 58%)": {104, 218, 78, 255},
		"hsl(359 65% 38%)": {160, 34, 36, 255},
	} {
		got, err := ParseColor(in)
		if err != nil || got != want {
			t.Errorf("%s = %v (%v), want %v", in, got, err, want)
		}
	}
}

func TestRefusesWhatItCannotRenderExactly(t *testing.T) {
	if _, err := PNG(sprite, 100, ""); err == nil {
		t.Error("100 is not a multiple of 8")
	}
	if _, err := PNG("<svg></svg>", 64, ""); err == nil {
		t.Error("an SVG with no pixels must be an error")
	}
	if _, err := PNG(strings.Replace(sprite, `x="1"`, `x="9"`, 1), 64, ""); err == nil {
		t.Error("a rect outside the grid must be an error")
	}
	if _, err := PNG(sprite, 64, "red"); err == nil {
		t.Error("a named colour is not supported")
	}
}

func TestBackgroundSVGPutsTheRectBehindTheSprite(t *testing.T) {
	out, err := Background(sprite, "#1F2A44", 512)
	if err != nil {
		t.Fatal(err)
	}
	bgAt, spriteAt := strings.Index(out, `fill="#1f2a44"`), strings.Index(out, `fill="#ff6b5b"`)
	if bgAt < 0 || spriteAt < 0 || bgAt > spriteAt {
		t.Fatalf("background must come first:\n%s", out)
	}
	if !strings.Contains(out, `width="512" height="512"`) || !strings.Contains(out, `viewBox="0 0 8 8"`) {
		t.Fatalf("size not applied:\n%s", out)
	}
	// The background rect must not be read back as a sprite pixel.
	if cs, _ := cells(out); len(cs) != 3 {
		t.Fatalf("expected 3 sprite cells, got %d", len(cs))
	}
}

func TestColorsInOrderOfAppearance(t *testing.T) {
	got := strings.Join(Colors(sprite), ",")
	if got != "#ff6b5b,hsl(200 65% 38%),#a7f3d0" {
		t.Fatalf("got %s", got)
	}
}
