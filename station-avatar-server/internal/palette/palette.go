// Package palette is the shared colour table for the avatar generators and the
// registry that hands each agent a slot in it.
//
// It is a port of the model-side generator's table and registry (avatar.py:
// CURATED, curated_pair) and reads and writes the same palette_registry.json, so
// the two can share one file. Order and slot arithmetic must stay identical to
// the Python: a slot index means the same colours to both.
package palette

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

// Pair is one curated (accent, background) pair.
type Pair struct {
	Name   string `json:"slot"`
	Accent string `json:"accent"`
	BG     string `json:"bg"`
}

// Curated is the table: distinct hues on dark, contrasting backgrounds. The
// order is part of the contract; never reorder, only append.
var Curated = []Pair{
	{"coral", "#FF6B5B", "#1F2A44"}, {"amber", "#FFB627", "#2B2D5B"}, {"lime", "#A6E22E", "#3B2A4D"}, {"mint", "#2EE6A6", "#3A2438"},
	{"cyan", "#3DDCF2", "#4A2C2A"}, {"sky", "#5AA9FF", "#2D3A2A"}, {"violet", "#9B6BFF", "#1D3B3A"}, {"magenta", "#FF4FA3", "#1E3A5F"},
	{"red", "#FF3B3B", "#14323A"}, {"orange", "#FF8A2B", "#263A5E"}, {"yellow", "#FFE14D", "#2E2A5E"}, {"teal", "#1FC8B8", "#4A2B4F"},
	{"pink", "#FF8FC7", "#243B4A"}, {"green", "#44D96B", "#3D2B3F"}, {"periwinkle", "#7C8CFF", "#3A3326"}, {"cream", "#F2E3A1", "#3B2B55"},
}

var (
	// ErrLocked: another writer held the registry lock for too long.
	ErrLocked = errors.New("palette registry is locked by another writer")
	// ErrCorrupt: the registry file exists but cannot be trusted. The Python
	// generator treats this as empty and overwrites it, silently reassigning
	// every colour; this side refuses instead.
	ErrCorrupt = errors.New("palette registry is unreadable")
	// ErrUnknownSlot: a name that is not in the table.
	ErrUnknownSlot = errors.New("unknown palette slot")
)

// ByName finds a slot by name.
func ByName(name string) (int, Pair, bool) {
	for i, p := range Curated {
		if strings.EqualFold(p.Name, name) {
			return i, p, true
		}
	}
	return 0, Pair{}, false
}

// Registry persists which slot each agent was given. The file is authoritative:
// losing it can reassign colours, so keep it with the avatars it describes.
type Registry struct {
	Path string
	// LockWait bounds how long Slot waits for another writer (default 2s).
	LockWait time.Duration
}

// Slot returns urn's slot, assigning and persisting one on first use. A slot is
// assigned by hashing the NFC-normalised urn and probing forward past taken
// slots, so the first len(Curated) agents never share one; after that the hash
// slot is shared.
func (r *Registry) Slot(urn string) (int, Pair, error) {
	urn = norm.NFC.String(urn)
	unlock, err := r.lock()
	if err != nil {
		return 0, Pair{}, err
	}
	defer unlock()

	reg, err := r.load()
	if err != nil {
		return 0, Pair{}, err
	}
	if idx, ok := reg[urn]; ok {
		return idx, Curated[idx], nil
	}
	idx := hashSlot(urn)
	used := map[int]bool{}
	for _, v := range reg {
		used[v] = true
	}
	for k := 0; k < len(Curated); k++ {
		if cand := (idx + k) % len(Curated); !used[cand] {
			idx = cand
			break
		}
	}
	reg[urn] = idx
	if err := r.save(reg); err != nil {
		return 0, Pair{}, err
	}
	return idx, Curated[idx], nil
}

// Peek returns urn's slot without assigning one.
func (r *Registry) Peek(urn string) (int, bool, error) {
	reg, err := r.load()
	if err != nil {
		return 0, false, err
	}
	idx, ok := reg[norm.NFC.String(urn)]
	return idx, ok, nil
}

// Lookup returns the pair urn has, or would be given first, without writing
// anything: a preview shows the colours an agent will get but does not take
// the slot. Before the agent is registered the answer is its hash slot, which
// registration may move if that slot is taken.
func (r *Registry) Lookup(urn string) (Pair, error) {
	idx, ok, err := r.Peek(urn)
	if err != nil {
		return Pair{}, err
	}
	if !ok {
		idx = hashSlot(norm.NFC.String(urn))
	}
	return Curated[idx], nil
}

func hashSlot(urn string) int {
	h := sha256.Sum256([]byte(urn))
	return (int(h[0])<<8 | int(h[1])) % len(Curated)
}

func (r *Registry) load() (map[string]int, error) {
	b, err := os.ReadFile(r.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]int{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	reg := map[string]int{}
	if err := json.Unmarshal(b, &reg); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, r.Path, err)
	}
	for urn, idx := range reg {
		if idx < 0 || idx >= len(Curated) {
			return nil, fmt.Errorf("%w: %s maps %q to slot %d, but the table has %d", ErrCorrupt, r.Path, urn, idx, len(Curated))
		}
	}
	return reg, nil
}

// save writes the registry the way the Python does (one-space indent, unicode
// kept) via a temp file and rename, so a reader never sees half a file.
func (r *Registry) save(reg map[string]int) error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(reg); err != nil {
		return err
	}
	tmp := r.Path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.Path)
}

// lock takes an exclusive lock file beside the registry. The Python writer takes
// none, so this only serialises this side's writers; a stale lock from a crashed
// process is broken after a minute.
func (r *Registry) lock() (func(), error) {
	wait := r.LockWait
	if wait <= 0 {
		wait = 2 * time.Second
	}
	path := r.Path + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, ErrLocked
		}
		time.Sleep(10 * time.Millisecond)
	}
}
