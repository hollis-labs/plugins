package sprite

import (
	"strings"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
)

type testCase struct {
	ID     string `json:"id"`
	Fill   int    `json:"fill"`
	Hue    int    `json:"hue"`
	Accent string `json:"accent"`
}

// cases is the matrix the golden file covers: unicode and astral-plane ids (the
// hash walks UTF-16 units), every fill extreme, the hue-derived colour path and
// all 16 curated accents (the shade rounds each channel).
func cases() []testCase {
	ids := []string{"agt-cogsworth", "agt-gizmo", "agt-rusty", "Ünï-cöde", "a", "x", "日本語", "😀emoji-𝒳", "agt-0", "agt-1", "a_b_c", "a/b:c",
		"é", strings.Repeat("z", 128)}
	var out []testCase
	for i, id := range ids {
		out = append(out, testCase{id, 65, -1, palette.Curated[i%16].Accent})
	}
	for _, id := range ids[:4] {
		for _, fill := range []int{0, 35, 100} {
			out = append(out, testCase{id, fill, -1, "#FF6B5B"})
		}
	}
	for _, id := range ids[:3] {
		for _, hue := range []int{-1, 0, 200, 359} {
			out = append(out, testCase{id, 65, hue, ""})
		}
	}
	for _, p := range palette.Curated {
		out = append(out, testCase{"agt-1", 65, -1, p.Accent})
	}
	out = append(out, testCase{"agt-1", 65, -1, "#010203"}, testCase{"agt-1", 65, -1, "#fEdCbA"}, testCase{"agt-1", 65, -1, "#FFFFFF"})
	return out
}
