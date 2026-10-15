// Package render turns the avatar workflow's SVG into PNGs, and applies a flat
// background. The PNG is read straight from the SVG's rects, so the two formats
// cannot disagree, and scaling is nearest-neighbour by an integer factor only.
package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"
)

// Grid is the sprite's native size: the workflow draws on an 8x8 grid.
const Grid = 8

var (
	rectRe = regexp.MustCompile(`<rect\b([^>]*)/>`)
	attrRe = regexp.MustCompile(`(\w+)="([^"]*)"`)
	hslRe  = regexp.MustCompile(`^hsl\((\d+) (\d+)% (\d+)%\)$`)
)

type cell struct {
	x, y int
	c    color.NRGBA
}

// cells reads every unit rect from svg. Anything else (a background rect that
// covers the grid) is ignored here: the background is applied by Background/PNG.
func cells(svg string) ([]cell, error) {
	var out []cell
	for _, m := range rectRe.FindAllStringSubmatch(svg, -1) {
		a := map[string]string{}
		for _, kv := range attrRe.FindAllStringSubmatch(m[1], -1) {
			a[kv[1]] = kv[2]
		}
		if a["width"] != "1" || a["height"] != "1" {
			continue
		}
		x, errx := strconv.Atoi(a["x"])
		y, erry := strconv.Atoi(a["y"])
		if errx != nil || erry != nil || x < 0 || y < 0 || x >= Grid || y >= Grid {
			return nil, fmt.Errorf("render: rect outside the %dx%d grid: %s", Grid, Grid, m[0])
		}
		c, err := ParseColor(a["fill"])
		if err != nil {
			return nil, err
		}
		out = append(out, cell{x, y, c})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("render: no sprite pixels in the SVG")
	}
	return out, nil
}

// ParseColor reads "#rrggbb" or the workflow's "hsl(h s% l%)".
func ParseColor(s string) (color.NRGBA, error) {
	if len(s) == 7 && s[0] == '#' {
		v, err := strconv.ParseUint(s[1:], 16, 32)
		if err != nil {
			return color.NRGBA{}, fmt.Errorf("render: bad colour %q", s)
		}
		return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
	}
	if m := hslRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		sat, _ := strconv.Atoi(m[2])
		l, _ := strconv.Atoi(m[3])
		return hsl(float64(h), float64(sat)/100, float64(l)/100), nil
	}
	return color.NRGBA{}, fmt.Errorf("render: unsupported colour %q", s)
}

func hsl(h, s, l float64) color.NRGBA {
	c := (1 - abs(2*l-1)) * s
	hp := h / 60
	x := c * (1 - abs(mod(hp, 2)-1))
	var r, g, b float64
	switch int(hp) % 6 {
	case 0:
		r, g, b = c, x, 0
	case 1:
		r, g, b = x, c, 0
	case 2:
		r, g, b = 0, c, x
	case 3:
		r, g, b = 0, x, c
	case 4:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := l - c/2
	return color.NRGBA{round((r + m) * 255), round((g + m) * 255), round((b + m) * 255), 255}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
func mod(v, m float64) float64 {
	for v >= m {
		v -= m
	}
	return v
}
func round(v float64) uint8 { return uint8(v + 0.5) }

// PNG renders svg at px by px. px must be a multiple of the native grid so the
// scale is an integer. bg is "#rrggbb" for a flat background, or "" for transparent.
func PNG(svg string, px int, bg string) ([]byte, error) {
	if px <= 0 || px%Grid != 0 {
		return nil, fmt.Errorf("render: %d px is not an integer multiple of the %d-cell grid", px, Grid)
	}
	cs, err := cells(svg)
	if err != nil {
		return nil, err
	}
	var bgc color.NRGBA
	if bg != "" {
		if bgc, err = ParseColor(bg); err != nil {
			return nil, err
		}
	}
	scale := px / Grid
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	if bg != "" {
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bgc.R, bgc.G, bgc.B, 255
		}
	}
	for _, c := range cs {
		for dy := 0; dy < scale; dy++ {
			for dx := 0; dx < scale; dx++ {
				img.SetNRGBA(c.x*scale+dx, c.y*scale+dy, c.c)
			}
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Background returns svg with a flat #rrggbb background behind the sprite, and
// width/height set to px (the viewBox keeps it crisp at any size).
func Background(svg, bg string, px int) (string, error) {
	if _, err := ParseColor(bg); err != nil {
		return "", err
	}
	i := strings.Index(svg, ">")
	if !strings.HasPrefix(svg, "<svg") || i < 0 {
		return "", fmt.Errorf("render: not an SVG document")
	}
	head := svg[:i+1]
	head = regexp.MustCompile(`width="\d+" height="\d+"`).ReplaceAllString(head, fmt.Sprintf(`width="%d" height="%d"`, px, px))
	return head + fmt.Sprintf(`<rect x="0" y="0" width="%d" height="%d" fill="%s"/>`, Grid, Grid, strings.ToLower(bg)) + svg[i+1:], nil
}

// Colors lists the distinct fills in svg, in order of first appearance.
func Colors(svg string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`fill="([^"]+)"`).FindAllStringSubmatch(svg, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}
