// Package store is where avatars live on disk: one PNG, an 8x PNG, an optional
// SVG and a JSON sidecar per agent, named after the agent.
//
// The stored file is the source of truth. Model output is not reproducible, so
// "does the file exist" is what makes generation idempotent; a regenerate keeps
// the old files under a timestamped name rather than deleting them.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// MaxNameBytes bounds a file stem; most filesystems allow 255 bytes per name and
// the store adds a suffix and, on archive, a timestamp.
const MaxNameBytes = 128

// ErrInvalidURN: the name cannot be turned into a safe file stem.
var ErrInvalidURN = errors.New("invalid urn")

// ErrCollision: two different URNs sanitise to the same file name.
var ErrCollision = errors.New("urn collides with another agent's file name")

// ErrNotFound: the agent has no stored avatar.
var ErrNotFound = errors.New("no avatar stored for that urn")

// SafeName is the file stem for urn: NFC-normalised, unicode letters and digits
// kept, and anything else outside . @ + - _ replaced by an underscore. It matches
// the model-side generator's safe_name so both write the same file.
func SafeName(urn string) (string, error) {
	n := norm.NFC.String(urn)
	if strings.TrimSpace(n) == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidURN)
	}
	var b strings.Builder
	for _, r := range n {
		if r == '_' || strings.ContainsRune(".@+-", r) || unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > MaxNameBytes {
		return "", fmt.Errorf("%w: %d bytes after sanitising, limit %d", ErrInvalidURN, len(name), MaxNameBytes)
	}
	if strings.HasPrefix(name, ".") {
		// Would hide the file, and ".." / "." must never reach a path.
		return "", fmt.Errorf("%w: a name may not start with a dot", ErrInvalidURN)
	}
	return name, nil
}

// Store is a directory of avatars.
type Store struct{ Dir string }

// Paths are one agent's files.
type Paths struct{ PNG, PNG8x, SVG, JSON string }

// Paths returns where name's files live.
func (s *Store) Paths(name string) Paths {
	return Paths{
		PNG:   filepath.Join(s.Dir, name+".png"),
		PNG8x: filepath.Join(s.Dir, name+"@8x.png"),
		SVG:   filepath.Join(s.Dir, name+".svg"),
		JSON:  filepath.Join(s.Dir, name+".json"),
	}
}

// Palette is the colours an avatar was drawn with.
type Palette struct {
	Slot   string   `json:"slot,omitempty"`
	Accent string   `json:"accent,omitempty"`
	BG     string   `json:"bg,omitempty"`
	Colors []string `json:"colors,omitempty"`
}

// Provenance is what produced a file; it is stored under "avatar" in the sidecar.
type Provenance struct {
	URN              string  `json:"urn"`
	RequestedMode    string  `json:"requested_mode"`
	UsedMode         string  `json:"used_mode"`
	FallbackReason   string  `json:"fallback_reason,omitempty"`
	FallbackDetail   string  `json:"fallback_detail,omitempty"`
	GeneratorVersion string  `json:"generator_version"`
	Model            string  `json:"model,omitempty"`
	CostUSD          float64 `json:"cost_usd"`
	PromptSHA256     string  `json:"prompt_sha256,omitempty"`
	GoldenRefSHA256  string  `json:"golden_ref_sha256,omitempty"`
	IdentityRefSHA   string  `json:"identity_ref_sha256,omitempty"`
	Palette          Palette `json:"palette"`
	Despeckle        bool    `json:"despeckle,omitempty"`
	HadronRunID      string  `json:"hadron_run_id,omitempty"`
	Fill             int     `json:"fill,omitempty"`
	Reproducible     bool    `json:"reproducible"`
	CreatedAt        string  `json:"created_at"`
}

// Sidecar is a decoded <name>.json: our provenance plus whatever the model-side
// tool wrote, which is kept untouched.
type Sidecar struct {
	Avatar *Provenance
	Other  map[string]json.RawMessage
}

// Exists reports whether name's canonical PNG is already stored.
func (s *Store) Exists(name string) bool {
	_, err := os.Stat(s.Paths(name).PNG)
	return err == nil
}

// ReadSidecar reads <name>.json. A missing file is (nil, nil); a file that does
// not parse is an error.
func (s *Store) ReadSidecar(name string) (*Sidecar, error) {
	b, err := os.ReadFile(s.Paths(name).JSON)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("store: %s: %w", s.Paths(name).JSON, err)
	}
	sc := &Sidecar{Other: all}
	if raw, ok := all["avatar"]; ok {
		var p Provenance
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("store: %s: avatar: %w", s.Paths(name).JSON, err)
		}
		sc.Avatar = &p
		delete(sc.Other, "avatar")
	}
	return sc, nil
}

// OwnedBy reports whether the stored files for name belong to urn. A file with
// no sidecar, or one that does not say whose it is, is treated as owned.
func (s *Store) OwnedBy(name, urn string) (bool, error) {
	sc, err := s.ReadSidecar(name)
	if err != nil || sc == nil {
		return true, err
	}
	have := ""
	if sc.Avatar != nil {
		have = sc.Avatar.URN
	} else if raw, ok := sc.Other["urn"]; ok {
		_ = json.Unmarshal(raw, &have)
	}
	return have == "" || norm.NFC.String(have) == norm.NFC.String(urn), nil
}

// Write stores one avatar. The sidecar goes last, so a reader that finds it can
// trust the images beside it. extra is the model-side tool's own sidecar fields.
func (s *Store) Write(name string, png, png8x []byte, svg string, prov Provenance, extra map[string]json.RawMessage) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	p := s.Paths(name)
	if err := atomic(p.PNG, png); err != nil {
		return err
	}
	if png8x != nil {
		if err := atomic(p.PNG8x, png8x); err != nil {
			return err
		}
	}
	if svg != "" {
		if err := atomic(p.SVG, []byte(svg)); err != nil {
			return err
		}
	}
	doc := map[string]any{}
	for k, v := range extra {
		doc[k] = v
	}
	doc["urn"] = prov.URN
	doc["avatar"] = prov
	b, err := marshal(doc)
	if err != nil {
		return err
	}
	return atomic(p.JSON, b)
}

// Archive moves name's existing files aside as <name>.<ts>.<ext> and returns the
// new PNG path, so a forced regenerate never destroys the old face.
func (s *Store) Archive(name, ts string) (string, error) {
	p := s.Paths(name)
	dst := func(stem, suffix string) string { return filepath.Join(s.Dir, stem+"."+ts+suffix) }
	moves := []struct{ from, to string }{
		{p.PNG, dst(name, ".png")}, {p.PNG8x, dst(name, "@8x.png")}, {p.SVG, dst(name, ".svg")}, {p.JSON, dst(name, ".json")},
	}
	for _, m := range moves {
		if _, err := os.Stat(m.to); err == nil {
			return "", fmt.Errorf("store: archive target already exists: %s", m.to)
		}
	}
	for _, m := range moves {
		if err := os.Rename(m.from, m.to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return moves[0].to, nil
}

func atomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func marshal(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}
