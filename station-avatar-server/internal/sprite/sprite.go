// Package sprite draws the 8x8 pixel-robot avatar natively. It is a line-for-line
// port of the script in workflows/avatar-generate.workflow.yaml, which stays the
// reference: golden_test.go holds that workflow's own output and the two must
// agree byte for byte, because a stored avatar's face is whatever this draws.
//
// Three JavaScript behaviours are reproduced on purpose: the seed hash walks
// UTF-16 code units (not bytes or runes), Math.imul and >>>0 are uint32
// arithmetic, and Math.round rounds halves up.
package sprite

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

const size = 8

// Sprite is a drawn robot: which cells are lit, where the eyes are, and the hue
// it was seeded with.
type Sprite struct {
	Pixels [size][size]bool
	Eyes   [2][2]int // {row, col}
	Hue    int
}

func hashString(s string) uint32 {
	h := uint32(2166136261)
	for _, u := range utf16.Encode([]rune(s)) {
		h ^= uint32(u)
		h *= 16777619
	}
	return h
}

// Draw builds the sprite for id. fill is 0-100; hue is 0-359, or -1 to keep the
// hue seeded from id.
func Draw(id string, fill, hue int) Sprite {
	seed := hashString(id)
	next := func() float64 {
		seed = seed*1664525 + 1013904223
		return float64(seed) / 4294967296
	}
	density := float64(100-fill) / 100

	var half [size][4]bool
	half[0][3] = true
	if next() > 0.5 {
		half[0][0] = true
	}
	for r := 1; r <= 4; r++ {
		for c := 1; c < 4; c++ {
			half[r][c] = true
		}
	}
	if next() > 0.5 {
		half[2][0], half[3][0] = true, true
	}
	for r := 5; r < size; r++ {
		half[r][3], half[r][2] = true, true
		half[r][1] = next() > density
		half[r][0] = next() > 0.55
	}

	var s Sprite
	for r := 0; r < size; r++ {
		for c := 0; c < 4; c++ {
			s.Pixels[r][c] = half[r][c]
			s.Pixels[r][size-1-c] = half[r][c]
		}
	}
	s.Eyes = [2][2]int{{2, 2}, {2, 5}}
	s.Hue = int(math.Floor(next() * 360))
	if hue >= 0 {
		s.Hue = hue
	}
	return s
}

func hex2(v int) string { return fmt.Sprintf("%02x", v) }

// jsRound is Math.round: halves round up.
func jsRound(x float64) int { return int(math.Floor(x + 0.5)) }

// shades returns the body and shade colours. accent is "#rrggbb", or "" for the
// colours derived from the sprite's hue.
func shades(s Sprite, accent string) (body, shade string) {
	if accent == "" {
		h := strconv.Itoa(s.Hue)
		return "hsl(" + h + " 65% 58%)", "hsl(" + h + " 65% 38%)"
	}
	dark := "#"
	for i := 1; i < 7; i += 2 {
		v, _ := strconv.ParseInt(accent[i:i+2], 16, 32)
		dark += hex2(jsRound(float64(v) * 0.6))
	}
	return strings.ToLower(accent), dark
}

// SVG renders s at 64px. The lower three rows use the shade colour.
func SVG(s Sprite, accent string) string {
	body, shade := shades(s, accent)
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" width="64" height="64" shape-rendering="crispEdges">`)
	for r := 0; r < size; r++ {
		for c := 0; c < size; c++ {
			if !s.Pixels[r][c] || s.isEye(r, c) {
				continue
			}
			fill := body
			if r >= 5 {
				fill = shade
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="1" height="1" fill="%s"/>`, c, r, fill)
		}
	}
	for _, e := range s.Eyes {
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="1" height="1" fill="#a7f3d0"/>`, e[1], e[0])
	}
	b.WriteString(`</svg>`)
	return b.String()
}

func (s Sprite) isEye(r, c int) bool {
	for _, e := range s.Eyes {
		if e[0] == r && e[1] == c {
			return true
		}
	}
	return false
}

// JSON is the sprite in the workflow's output shape.
func (s Sprite) JSON() json.RawMessage {
	rows := make([][]bool, size)
	for r := range rows {
		rows[r] = s.Pixels[r][:]
	}
	b, _ := json.Marshal(map[string]any{"pixels": rows, "eyes": s.Eyes, "hue": s.Hue})
	return b
}
