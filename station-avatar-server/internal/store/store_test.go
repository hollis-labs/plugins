package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestSafeNameMatchesThePythonRule(t *testing.T) {
	// avatar.py: re.sub(r"[^\w.@+-]", "_", NFC(urn)).
	cases := map[string]string{
		"agt-cogsworth":             "agt-cogsworth",
		"Ünï-cöde":                  "Ünï-cöde",
		norm.NFD.String("Ünï-cöde"): "Ünï-cöde", // NFD input is normalised to NFC first
		"team worker":               "team_worker",
		"a/b:c":                     "a_b_c",
		"agent@host+1.2":            "agent@host+1.2",
		"名前-01":                     "名前-01",
		"../../etc/passwd":          "_.._etc_passwd",
	}
	for in, want := range cases {
		got, err := SafeName(in)
		if want == "_.._etc_passwd" {
			// Leading ".." is refused outright rather than sanitised into a path piece.
			if err == nil && strings.Contains(got, "/") {
				t.Errorf("%q produced a path separator: %q", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("SafeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestSafeNameRefusesWhatCannotBeAFile(t *testing.T) {
	for _, bad := range []string{"", "   ", ".", "..", ".hidden", strings.Repeat("x", MaxNameBytes+1)} {
		if _, err := SafeName(bad); !errors.Is(err, ErrInvalidURN) {
			t.Errorf("SafeName(%q): got %v, want ErrInvalidURN", bad, err)
		}
	}
}

func TestNoNameEverEscapesTheDirectory(t *testing.T) {
	for _, in := range []string{"a/../b", "..\\x", "x\x00y", "/abs", "a\nb"} {
		n, err := SafeName(in)
		if err != nil {
			continue
		}
		if strings.ContainsAny(n, "/\\\x00\n") {
			t.Errorf("SafeName(%q) = %q contains a path or control character", in, n)
		}
	}
}

func TestWriteThenReadBack(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	prov := Provenance{URN: "agt-1", RequestedMode: "svg", UsedMode: "svg", GeneratorVersion: "v2", Reproducible: true, CreatedAt: "t",
		Palette: Palette{Slot: "coral", Accent: "#FF6B5B", BG: "#1F2A44"}}
	extra := map[string]json.RawMessage{"model": json.RawMessage(`"gemini"`), "palette": json.RawMessage(`["#000000"]`)}
	if err := s.Write("agt-1", []byte("png"), []byte("png8"), "<svg/>", prov, extra); err != nil {
		t.Fatal(err)
	}
	if !s.Exists("agt-1") {
		t.Fatal("Exists must see the PNG")
	}
	sc, err := s.ReadSidecar("agt-1")
	if err != nil || sc == nil || sc.Avatar == nil || sc.Avatar.Palette.Slot != "coral" {
		t.Fatalf("read back: %+v, %v", sc, err)
	}
	// The model tool's own fields survive untouched, including one named like ours.
	compact := func(r json.RawMessage) string {
		var b bytes.Buffer
		if err := json.Compact(&b, r); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if compact(sc.Other["model"]) != `"gemini"` || compact(sc.Other["palette"]) != `["#000000"]` {
		t.Fatalf("the model-side sidecar fields were not preserved: %s", sc.Other)
	}
	if _, err := os.Stat(s.Paths("agt-1").SVG); err != nil {
		t.Fatal("svg not written")
	}
}

func TestReadSidecarOfAFileWithoutOneIsNil(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	if sc, err := s.ReadSidecar("nope"); sc != nil || err != nil {
		t.Fatalf("got %v, %v", sc, err)
	}
	os.WriteFile(s.Paths("bad").JSON, []byte("{nope"), 0o644)
	if _, err := s.ReadSidecar("bad"); err == nil {
		t.Fatal("a corrupt sidecar must be an error, not silently empty")
	}
}

func TestOwnershipCatchesCollidingNames(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	name, _ := SafeName("a/b:c")
	if n2, _ := SafeName("a_b_c"); n2 != name {
		t.Fatalf("test premise: %q and %q must collide", name, n2)
	}
	s.Write(name, []byte("png"), nil, "", Provenance{URN: "a/b:c", UsedMode: "svg"}, nil)
	if ok, _ := s.OwnedBy(name, "a/b:c"); !ok {
		t.Fatal("the owner must be recognised")
	}
	if ok, _ := s.OwnedBy(name, "a_b_c"); ok {
		t.Fatal("a different urn mapping to the same file name must be refused")
	}
	// The model tool's sidecar names the agent in a top-level "urn".
	os.WriteFile(s.Paths("py").JSON, []byte(`{"urn":"py-agent","model":"m"}`), 0o644)
	if ok, _ := s.OwnedBy("py", "py-agent"); !ok {
		t.Fatal("a Python-written sidecar's urn must count")
	}
	if ok, _ := s.OwnedBy("py", "someone-else"); ok {
		t.Fatal("a Python-written sidecar belonging to someone else must be refused")
	}
	if ok, _ := s.OwnedBy("no-sidecar", "x"); !ok {
		t.Fatal("no sidecar means nothing to contradict")
	}
}

func TestArchiveKeepsTheOldFace(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	s.Write("agt-1", []byte("old"), []byte("old8"), "<svg/>", Provenance{URN: "agt-1"}, nil)
	moved, err := s.Archive("agt-1", "20261015T004500Z")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(moved) != "agt-1.20261015T004500Z.png" {
		t.Fatalf("archived as %s", moved)
	}
	if b, _ := os.ReadFile(moved); string(b) != "old" {
		t.Fatal("the archived file must be the old one")
	}
	if s.Exists("agt-1") {
		t.Fatal("the canonical name must be free for the new face")
	}
	for _, f := range []string{"agt-1.20261015T004500Z@8x.png", "agt-1.20261015T004500Z.svg", "agt-1.20261015T004500Z.json"} {
		if _, err := os.Stat(filepath.Join(s.Dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	// Archiving again with the same stamp must not overwrite.
	s.Write("agt-1", []byte("new"), nil, "", Provenance{URN: "agt-1"}, nil)
	if _, err := s.Archive("agt-1", "20261015T004500Z"); err == nil {
		t.Fatal("an existing archive must never be overwritten")
	}
	if b, _ := os.ReadFile(moved); string(b) != "old" {
		t.Fatal("the first archive was clobbered")
	}
}
