// Package service is the avatar generator's brain: it takes one request, picks a
// palette, walks the policy's chain of engines for the requested mode, gates
// paid engines against the spend limits, and stores the result. It is
// transport-free; the MCP tools are a thin layer over Generate.
//
// It does not know which engines exist. Each declares its capabilities (does it
// cost money, can it be reproduced, does it want the agent's sprite) and the
// service decides from those and a policy:
//
//	svg     the procedural sprite (a Hadron workflow). Free, fast, reproducible.
//	image   a model with the golden style reference. Costs money; not reproducible.
//	hybrid  a model shown the agent's own procedural sprite as its identity.
//
// The stored file is the source of truth. Asking again for an agent that already
// has one returns it and spends nothing, unless force is set.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/engine"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/policy"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/render"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/spend"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

// Mode is how an avatar is made.
type Mode = engine.Mode

const (
	ModeSVG    = engine.SVG
	ModeImage  = engine.Image
	ModeHybrid = engine.Hybrid
)

const (
	// PNGPx is the canonical PNG size; PNG8xPx is the 8x copy. Both are exact
	// integer multiples of the 8-cell sprite grid.
	PNGPx   = 64
	PNG8xPx = 512
	// MaxLabel bounds role/species/mood, which reach a model prompt.
	MaxLabel = 64
)

// Service generates and stores avatars.
type Service struct {
	Store    *store.Store
	Registry *palette.Registry
	// Engines are the available adapters by mode. A mode with no engine is
	// unavailable (model_unconfigured), which the policy's fallback covers.
	Engines map[engine.Mode]engine.Engine
	Policy  policy.Policy
	// Ledger records paid attempts and backs the daily and total limits.
	Ledger *spend.Ledger
	Now    func() time.Time
	// Previewer draws unsaved svg previews; nil disables Preview.
	Previewer SVGSource
	// PublicURL is the base of the read-only HTTP surface (no trailing slash).
	// When set, responses carry URLs a browser can load; empty omits them.
	PublicURL string

	mu    sync.Mutex
	names map[string]*nameLock
	pay   sync.Mutex // paid attempts run one at a time, so limits cannot be raced past
}

type nameLock struct {
	mu   sync.Mutex
	refs int
}

// Request is one avatar to generate.
type Request struct {
	URN      string
	Mode     Mode   // default hybrid
	Fallback string // "svg" (default: follow the policy) or "none"
	Force    bool
	// Role, Species and Mood vary a model's drawing; they have no effect in svg mode.
	Role, Species, Mood string
	// Palette is a slot name from the curated table; Accent and BG together give
	// an explicit pair. With none of these the agent's registry slot is used.
	Palette    string
	Accent, BG string
	// Fill is the procedural sprite's body fill in percent; nil means the default.
	Fill      *int
	BudgetUSD float64
}

// File is one stored file.
type File struct {
	Path       string `json:"path"`
	Format     string `json:"format"`
	Px         int    `json:"px"`
	NativeGrid int    `json:"native_grid"`
	Role       string `json:"role"`
}

// Response is the contract's answer.
type Response struct {
	URN            string  `json:"urn"`
	RequestedMode  Mode    `json:"requested_mode"`
	UsedMode       Mode    `json:"used_mode"`
	FallbackReason *string `json:"fallback_reason"`
	FallbackDetail string  `json:"fallback_detail,omitempty"`
	Existed        bool    `json:"existed"`
	Files          []File  `json:"files"`
	Sidecar        string  `json:"sidecar"`
	// URLs are browser-loadable addresses of the files, by role, when the HTTP
	// surface is configured.
	URLs         map[string]string `json:"urls,omitempty"`
	Provenance   store.Provenance  `json:"provenance"`
	Reproducible bool              `json:"reproducible"`
}

// ModeError is a request no engine in its chain could serve: fallback was
// disabled, or the policy's chain had nothing free to end on.
type ModeError struct {
	Requested Mode
	Reason    engine.Reason
	Detail    string
}

func (e *ModeError) Error() string {
	return fmt.Sprintf("%s mode could not run (%s): %s", e.Requested, e.Reason, e.Detail)
}

// Generate makes (or returns) the avatar for r.URN.
func (s *Service) Generate(ctx context.Context, r Request) (*Response, error) {
	if err := s.normalize(&r); err != nil {
		return nil, err
	}
	name, err := store.SafeName(r.URN)
	if err != nil {
		return nil, &avatar.FieldError{Field: "urn", Msg: err.Error()}
	}
	unlock := s.lock(name)
	defer unlock()

	if ok, err := s.Store.OwnedBy(name, r.URN); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%w: %q maps to %s, which another agent already has", store.ErrCollision, r.URN, name)
	}

	exists := s.Store.Exists(name)
	if exists && !r.Force {
		resp, err := s.existing(name, r)
		if err != nil || resp != nil {
			return resp, err
		}
		// A stand-in the requested engine can now replace: fall through and regenerate.
	}

	pal, slot, err := s.resolvePalette(r)
	if err != nil {
		return nil, err
	}
	fill := avatar.FillDefault
	if r.Fill != nil {
		fill = *r.Fill
	}
	urn := norm.NFC.String(r.URN)
	call := engine.Call{URN: urn, Name: name, Role: r.Role, Species: r.Species, Mood: r.Mood,
		Accent: strings.ToUpper(pal.Accent), BG: strings.ToUpper(pal.BG), Slot: slot, Fill: fill}

	var first *engine.Unavailable // why the requested mode did not run, if it did not
	var ident *engine.Identity    // the agent's sprite, drawn at most once per request
	for _, mode := range s.Policy.Chain(r.Mode, r.Fallback != "none") {
		out, caps, un, err := s.attempt(ctx, mode, r, &call, &ident)
		if err != nil {
			return nil, err
		}
		if un != nil {
			if first == nil {
				first = un
			}
			continue
		}
		prov := store.Provenance{
			URN: urn, RequestedMode: string(r.Mode), UsedMode: string(mode), GeneratorVersion: avatar.GeneratorVersion,
			Palette: store.Palette{Slot: slot, Accent: call.Accent, BG: call.BG, Colors: out.Colors}, Fill: fill,
			Model: out.Model, CostUSD: out.CostUSD, PromptSHA256: out.PromptSHA256, GoldenRefSHA256: out.GoldenSHA256,
			IdentityRefSHA: out.IdentitySHA256, Despeckle: out.Despeckle, HadronRunID: out.HadronRunID,
			Reproducible: caps.Reproducible, CreatedAt: s.now().UTC().Format(time.RFC3339),
		}
		if first != nil {
			prov.FallbackReason, prov.FallbackDetail = string(first.Reason), first.Detail
		}
		if exists {
			if _, err := s.Store.Archive(name, s.now().UTC().Format("20060102T150405.000Z")); err != nil {
				return nil, err
			}
		}
		if err := s.Store.Write(name, out.PNG, out.PNG8x, out.SVG, prov, out.Extra); err != nil {
			return nil, err
		}
		return s.response(name, r, prov, false), nil
	}
	return nil, &ModeError{Requested: r.Mode, Reason: first.Reason, Detail: first.Detail}
}

// attempt tries one engine. It returns an output, or why this engine could not
// serve the request (a failure the policy may cover), or an error the caller must
// see. Paid engines are gated against the spend limits first, and their cost,
// including the cost of a failed attempt, is recorded.
func (s *Service) attempt(ctx context.Context, mode engine.Mode, r Request, call *engine.Call, ident **engine.Identity) (*engine.Output, engine.Capabilities, *engine.Unavailable, error) {
	eng := s.Engines[mode]
	if eng == nil {
		return nil, engine.Capabilities{}, &engine.Unavailable{Reason: engine.Unconfigured, Detail: fmt.Sprintf("no %s engine is configured", mode)}, nil
	}
	caps := eng.Capabilities()
	if un := eng.Preflight(); un != nil {
		return nil, caps, un, nil
	}
	if caps.Paid {
		s.pay.Lock()
		defer s.pay.Unlock()
		un, err := s.gate(r, caps)
		if err != nil || un != nil {
			return nil, caps, un, err
		}
	}

	c := *call
	switch {
	case caps.NeedsIdentity:
		if *ident == nil {
			prov := s.identityProvider()
			if prov == nil {
				return nil, caps, &engine.Unavailable{Reason: engine.Unconfigured, Detail: "no engine can draw the agent's sprite for " + string(mode)}, nil
			}
			id, err := prov.Identity(ctx, c)
			if err != nil {
				return nil, caps, nil, err
			}
			*ident = id
		}
		c.Identity = *ident
	case *ident != nil:
		c.Identity = *ident // an engine that can reuse the sprite already drawn
	}

	out, err := eng.Generate(ctx, c)
	var un *engine.Unavailable
	if errors.As(err, &un) {
		if caps.Paid {
			s.record(r, mode, "failed: "+string(un.Reason), un.CostUSD)
		}
		return nil, caps, un, nil
	}
	if err != nil {
		return nil, caps, nil, err
	}
	if caps.Paid {
		s.record(r, mode, out.Model, out.CostUSD)
	}
	return out, caps, nil, nil
}

// gate refuses a paid attempt that would break a spend limit. A ledger that
// cannot be read while a daily or total limit is set is an error, not a pass:
// the limit cannot be enforced.
func (s *Service) gate(r Request, caps engine.Capabilities) (*engine.Unavailable, error) {
	sp := s.Policy.Spend
	over := func(format string, a ...any) *engine.Unavailable {
		return &engine.Unavailable{Reason: engine.OverBudget, Detail: fmt.Sprintf(format, a...)}
	}
	if c := sp.Ceiling(r.BudgetUSD); c > 0 && caps.EstimateUSD > c {
		return over("estimated $%.3f exceeds the $%.3f per-call ceiling", caps.EstimateUSD, c), nil
	}
	if sp.DailyUSD == 0 && sp.TotalUSD == 0 {
		return nil, nil
	}
	if s.Ledger == nil {
		return nil, errors.New("spend limits are set but there is no spend ledger")
	}
	t, err := s.Ledger.Totals(s.now())
	if err != nil {
		return nil, err
	}
	if sp.DailyUSD > 0 && t.Today+caps.EstimateUSD > sp.DailyUSD {
		return over("$%.3f spent today plus an estimated $%.3f would exceed the $%.2f daily limit", t.Today, caps.EstimateUSD, sp.DailyUSD), nil
	}
	if sp.TotalUSD > 0 && t.All+caps.EstimateUSD > sp.TotalUSD {
		return over("$%.3f spent in total plus an estimated $%.3f would exceed the $%.2f total limit", t.All, caps.EstimateUSD, sp.TotalUSD), nil
	}
	return nil, nil
}

// record notes a paid attempt. A failure to write the ledger cannot undo money
// already spent, so it is logged loudly rather than failing a request whose
// avatar was drawn.
func (s *Service) record(r Request, mode engine.Mode, info string, usd float64) {
	if s.Ledger == nil {
		return
	}
	if err := s.Ledger.Record(spend.Entry{At: s.now().UTC(), URN: norm.NFC.String(r.URN), Mode: string(mode), Info: info, USD: usd}); err != nil {
		log.Printf("avatar: SPEND NOT RECORDED ($%.4f for %s, %s): %v", usd, r.URN, mode, err)
	}
}

func (s *Service) identityProvider() engine.IdentityProvider {
	if e, ok := s.Engines[engine.SVG].(engine.IdentityProvider); ok {
		return e
	}
	for _, e := range s.Engines {
		if p, ok := e.(engine.IdentityProvider); ok {
			return p
		}
	}
	return nil
}

// runnable reports whether the engine for mode could serve r right now.
func (s *Service) runnable(mode engine.Mode, r Request) bool {
	eng := s.Engines[mode]
	if eng == nil || eng.Preflight() != nil {
		return false
	}
	if eng.Capabilities().Paid {
		un, err := s.gate(r, eng.Capabilities())
		return un == nil && err == nil
	}
	return true
}

// existing answers for a stored avatar, or returns (nil, nil) when it is a
// stand-in that the requested engine could now replace.
func (s *Service) existing(name string, r Request) (*Response, error) {
	sc, err := s.Store.ReadSidecar(name)
	if err != nil {
		return nil, err
	}
	var prov store.Provenance
	if sc != nil && sc.Avatar != nil {
		prov = *sc.Avatar
		standIn := prov.FallbackReason != "" && prov.UsedMode != string(r.Mode)
		if standIn && s.runnable(r.Mode, r) {
			return nil, nil
		}
	} else {
		prov = store.Provenance{URN: norm.NFC.String(r.URN), UsedMode: "unknown", RequestedMode: string(r.Mode)}
	}
	return s.response(name, r, prov, true), nil
}

func (s *Service) response(name string, r Request, prov store.Provenance, existed bool) *Response {
	p := s.Store.Paths(name)
	var files []File
	add := func(path, format string, px, grid int, role string) {
		if fileExists(path) {
			files = append(files, File{Path: path, Format: format, Px: px, NativeGrid: grid, Role: role})
		}
	}
	grid := 64
	if prov.UsedMode == string(ModeSVG) {
		grid = render.Grid
	}
	add(p.PNG, "png", PNGPx, grid, "canonical")
	add(p.PNG8x, "png", PNG8xPx, grid, "upscaled")
	add(p.SVG, "svg", PNG8xPx, render.Grid, "vector")
	var reason *string
	if prov.FallbackReason != "" {
		v := prov.FallbackReason
		reason = &v
	}
	var urls map[string]string
	if s.PublicURL != "" {
		esc := url.PathEscape(name)
		urls = map[string]string{"png": s.PublicURL + "/avatars/" + esc + ".png", "png8x": s.PublicURL + "/avatars/" + esc + "@8x.png"}
		if fileExists(p.SVG) {
			urls["svg"] = s.PublicURL + "/avatars/" + esc + ".svg"
		}
	}
	return &Response{
		URLs: urls,
		URN:  prov.URN, RequestedMode: r.Mode, UsedMode: Mode(prov.UsedMode), FallbackReason: reason, FallbackDetail: prov.FallbackDetail,
		Existed: existed, Files: files, Sidecar: p.JSON, Provenance: prov, Reproducible: prov.Reproducible,
	}
}

// resolvePalette picks the colours: an explicit pair, a named slot, or the
// agent's registry slot (assigned on first use).
func (s *Service) resolvePalette(r Request) (palette.Pair, string, error) {
	switch {
	case r.Accent != "":
		return palette.Pair{Name: "custom", Accent: strings.ToUpper(r.Accent), BG: strings.ToUpper(r.BG)}, "custom", nil
	case r.Palette != "":
		_, p, _ := palette.ByName(r.Palette) // validated in normalize
		return p, p.Name, nil
	}
	_, p, err := s.Registry.Slot(r.URN)
	if err != nil {
		return palette.Pair{}, "", err
	}
	return p, p.Name, nil
}

func (s *Service) normalize(r *Request) error {
	bad := func(field, msg string) error { return &avatar.FieldError{Field: field, Msg: msg} }
	if strings.TrimSpace(r.URN) == "" {
		return bad("urn", "urn is required")
	}
	if len(r.URN) > avatar.MaxIDLen {
		return bad("urn", fmt.Sprintf("urn is %d bytes; the limit is %d", len(r.URN), avatar.MaxIDLen))
	}
	switch {
	case r.Mode == "":
		r.Mode = ModeHybrid
	case !r.Mode.Known():
		return bad("mode", fmt.Sprintf("mode must be svg, image or hybrid, got %q", r.Mode))
	}
	switch r.Fallback {
	case "":
		r.Fallback = "svg"
	case "svg", "none":
	default:
		return bad("fallback", fmt.Sprintf(`fallback must be "svg" or "none", got %q`, r.Fallback))
	}
	if r.Species == "" {
		r.Species = "robot"
	}
	for field, v := range map[string]*string{"role": &r.Role, "species": &r.Species, "mood": &r.Mood} {
		if err := cleanLabel(field, *v); err != nil {
			return err
		}
	}
	if r.Fill != nil && (*r.Fill < avatar.FillMin || *r.Fill > avatar.FillMax) {
		return bad("fill", fmt.Sprintf("fill must be an integer from %d to %d, got %d", avatar.FillMin, avatar.FillMax, *r.Fill))
	}
	if r.BudgetUSD < 0 || math.IsNaN(r.BudgetUSD) || math.IsInf(r.BudgetUSD, 0) {
		return bad("budget_usd", "budget_usd must be a non-negative number")
	}
	explicit := r.Accent != "" || r.BG != ""
	if explicit && (r.Accent == "" || r.BG == "") {
		return bad("accent", "accent and bg must be given together")
	}
	if explicit && r.Palette != "" {
		return bad("palette", "give either a palette slot or accent and bg, not both")
	}
	if explicit {
		if !avatar.ValidHex(r.Accent) {
			return bad("accent", fmt.Sprintf("accent must be #rrggbb, got %q", r.Accent))
		}
		if !avatar.ValidHex(r.BG) {
			return bad("bg", fmt.Sprintf("bg must be #rrggbb, got %q", r.BG))
		}
	}
	if r.Palette != "" {
		if _, _, ok := palette.ByName(r.Palette); !ok {
			names := make([]string, len(palette.Curated))
			for i, p := range palette.Curated {
				names[i] = p.Name
			}
			return bad("palette", fmt.Sprintf("unknown palette slot %q; slots: %s", r.Palette, strings.Join(names, ", ")))
		}
	}
	return nil
}

// cleanLabel keeps prompt inputs short and free of control characters.
func cleanLabel(field, v string) error {
	if len([]rune(v)) > MaxLabel {
		return &avatar.FieldError{Field: field, Msg: fmt.Sprintf("%s is longer than %d characters", field, MaxLabel)}
	}
	for _, c := range v {
		if unicode.IsControl(c) {
			return &avatar.FieldError{Field: field, Msg: field + " may not contain control characters or newlines"}
		}
	}
	return nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// lock serialises work on one agent's files; different agents run in parallel.
func (s *Service) lock(name string) func() {
	s.mu.Lock()
	if s.names == nil {
		s.names = map[string]*nameLock{}
	}
	l := s.names[name]
	if l == nil {
		l = &nameLock{}
		s.names[name] = l
	}
	l.refs++
	s.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.mu.Lock()
		if l.refs--; l.refs == 0 {
			delete(s.names, name)
		}
		s.mu.Unlock()
	}
}
