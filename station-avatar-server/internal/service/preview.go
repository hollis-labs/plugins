package service

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/render"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

// PreviewRequest asks for a throwaway svg avatar. Nothing is stored and no
// palette slot is taken, so it is safe to call on every keystroke.
type PreviewRequest struct {
	// Seed decides the face: the same seed, fill and colours always draw the same
	// avatar. It is the URN of the agent the preview is for, or any text.
	Seed string
	// Palette is a slot name; Accent and BG together are an explicit pair. With
	// neither, the colours are the ones the seed's agent has or would first get.
	Palette    string
	Accent, BG string
	// Fill is the body fill in percent; nil means the default.
	Fill *int
}

// Preview is a drawn, unsaved avatar.
type Preview struct {
	Seed    string        `json:"seed"`
	SVG     string        `json:"svg"`
	Palette store.Palette `json:"palette"`
	Fill    int           `json:"fill"`
	RunID   string        `json:"run_id"`

	sprite string // the sprite without its background, for PNG rendering
}

// PNG renders the preview at px (a multiple of the 8-cell grid).
func (p *Preview) PNG(px int) ([]byte, error) { return render.PNG(p.sprite, px, p.Palette.BG) }

// Preview draws an avatar without storing it. It is always the free svg engine.
func (s *Service) Preview(ctx context.Context, r PreviewRequest) (*Preview, error) {
	bad := func(field, msg string) error { return &avatar.FieldError{Field: field, Msg: msg} }
	if s.Previewer == nil {
		return nil, bad("seed", "no svg engine is configured")
	}
	seed := norm.NFC.String(r.Seed)
	if strings.TrimSpace(seed) == "" {
		return nil, bad("seed", "seed is required")
	}
	if len(seed) > avatar.MaxIDLen {
		return nil, bad("seed", fmt.Sprintf("seed is %d bytes; the limit is %d", len(seed), avatar.MaxIDLen))
	}
	// normalize validates the colour fields exactly as Generate does.
	n := Request{URN: seed, Palette: r.Palette, Accent: r.Accent, BG: r.BG, Fill: r.Fill}
	if err := s.normalize(&n); err != nil {
		return nil, err
	}
	var pal palette.Pair
	switch {
	case n.Accent != "":
		pal = palette.Pair{Name: "custom", Accent: n.Accent, BG: n.BG}
	case n.Palette != "":
		_, pal, _ = palette.ByName(n.Palette)
	default:
		var err error
		if pal, err = s.Registry.Lookup(seed); err != nil {
			return nil, err
		}
	}
	fill := avatar.FillDefault
	if r.Fill != nil {
		fill = *r.Fill
	}
	accent, bg := strings.ToUpper(pal.Accent), strings.ToUpper(pal.BG)
	res, err := s.Previewer.Generate(ctx, avatar.Request{ID: seed, Knobs: avatar.Knobs{Fill: fill, Hue: avatar.HueDefault}, Accent: accent})
	if err != nil {
		return nil, err
	}
	svg, err := render.Background(res.SVG, bg, PNG8xPx)
	if err != nil {
		return nil, err
	}
	return &Preview{
		Seed: seed, SVG: svg, Fill: fill, RunID: res.RunID, sprite: res.SVG,
		Palette: store.Palette{Slot: pal.Name, Accent: accent, BG: bg, Colors: append(render.Colors(res.SVG), strings.ToLower(bg))},
	}, nil
}

// Deleted is what Delete moved aside.
type Deleted struct {
	URN string `json:"urn"`
	// ArchivedTo is the timestamped PNG the old face was moved to; its other
	// files sit beside it under the same timestamp.
	ArchivedTo string `json:"archived_to"`
}

// Delete takes an agent's avatar out of service: its files are archived under a
// timestamped name, never destroyed, so the next avatar_generate makes a new face.
// The agent keeps its palette slot.
func (s *Service) Delete(_ context.Context, urn string) (*Deleted, error) {
	urn = norm.NFC.String(urn)
	name, err := store.SafeName(urn)
	if err != nil {
		return nil, err
	}
	defer s.lock(name)()
	if !s.Store.Exists(name) {
		return nil, fmt.Errorf("%w: %q", store.ErrNotFound, urn)
	}
	if ok, err := s.Store.OwnedBy(name, urn); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%w: %q maps to %s, which another agent has", store.ErrCollision, urn, name)
	}
	to, err := s.Store.Archive(name, s.now().UTC().Format("20060102T150405.000Z"))
	if err != nil {
		return nil, err
	}
	return &Deleted{URN: urn, ArchivedTo: to}, nil
}
