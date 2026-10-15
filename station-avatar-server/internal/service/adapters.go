package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/engine"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/model"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/render"
)

// SVGSource draws the procedural sprite (a Hadron workflow). HadronSVG is the
// real one.
type SVGSource interface {
	Generate(ctx context.Context, r avatar.Request) (SVGResult, error)
}

// SVGResult is the procedural source's output.
type SVGResult struct {
	SVG    string
	RunID  string
	Sprite []byte
}

type svgAdapter struct{ src SVGSource }

// NewSVG is the procedural engine: free, reproducible, and able to draw the
// agent's sprite for engines that want one.
func NewSVG(src SVGSource) engine.Engine { return svgAdapter{src} }

func (svgAdapter) Mode() engine.Mode { return engine.SVG }
func (svgAdapter) Capabilities() engine.Capabilities {
	return engine.Capabilities{Reproducible: true, ProvidesIdentity: true}
}
func (svgAdapter) Preflight() *engine.Unavailable { return nil }

func (a svgAdapter) sprite(ctx context.Context, c engine.Call) (SVGResult, error) {
	return a.src.Generate(ctx, avatar.Request{ID: c.URN, Knobs: avatar.Knobs{Fill: c.Fill, Hue: avatar.HueDefault}, Accent: c.Accent})
}

// Identity draws the sprite on a transparent background at 512px: an engine that
// wants the agent's silhouette reads it from alpha.
func (a svgAdapter) Identity(ctx context.Context, c engine.Call) (*engine.Identity, error) {
	res, err := a.sprite(ctx, c)
	if err != nil {
		return nil, err
	}
	png, err := render.PNG(res.SVG, PNG8xPx, "")
	if err != nil {
		return nil, err
	}
	return &engine.Identity{SVG: res.SVG, PNG: png, RunID: res.RunID}, nil
}

func (a svgAdapter) Generate(ctx context.Context, c engine.Call) (*engine.Output, error) {
	var res SVGResult
	if c.Identity != nil && c.Identity.SVG != "" { // already drawn for another engine this request: same inputs, same sprite
		res = SVGResult{SVG: c.Identity.SVG, RunID: c.Identity.RunID}
	} else {
		var err error
		if res, err = a.sprite(ctx, c); err != nil {
			return nil, err
		}
	}
	png, err := render.PNG(res.SVG, PNGPx, c.BG)
	if err != nil {
		return nil, err
	}
	png8x, err := render.PNG(res.SVG, PNG8xPx, c.BG)
	if err != nil {
		return nil, err
	}
	svg, err := render.Background(res.SVG, c.BG, PNG8xPx)
	if err != nil {
		return nil, err
	}
	return &engine.Output{PNG: png, PNG8x: png8x, SVG: svg, HadronRunID: res.RunID,
		Colors: append(render.Colors(res.SVG), strings.ToLower(c.BG))}, nil
}

type modelAdapter struct {
	mode     engine.Mode
	m        model.Engine
	estimate float64
	stage    string
}

// NewModel is a model-backed engine. mode is Image (the golden reference only) or
// Hybrid (also the agent's own sprite). estimateUSD is what one call is expected
// to cost; stageDir is where output is staged before the service commits it.
func NewModel(mode engine.Mode, m model.Engine, estimateUSD float64, stageDir string) engine.Engine {
	return modelAdapter{mode: mode, m: m, estimate: estimateUSD, stage: stageDir}
}

func (a modelAdapter) Mode() engine.Mode { return a.mode }
func (a modelAdapter) Capabilities() engine.Capabilities {
	return engine.Capabilities{Paid: true, EstimateUSD: a.estimate, NeedsIdentity: a.mode == engine.Hybrid}
}

// Preflight checks the tool, the credential and nothing else: spend is the
// service's to gate, from the policy and the ledger.
func (a modelAdapter) Preflight() *engine.Unavailable { return a.m.Preflight(0) }

func (a modelAdapter) Generate(ctx context.Context, c engine.Call) (*engine.Output, error) {
	if err := os.MkdirAll(a.stage, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(a.stage, ".stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	req := model.Request{URN: c.URN, Name: c.Name, Role: c.Role, Species: c.Species, Mood: c.Mood, Accent: c.Accent, BG: c.BG, OutDir: stage}
	if a.mode == engine.Hybrid {
		if c.Identity == nil {
			return nil, &engine.Unavailable{Reason: engine.ModelError, Detail: "hybrid mode needs the agent's sprite and was given none"}
		}
		req.IdentityPNG = filepath.Join(stage, "identity.png")
		if err := os.WriteFile(req.IdentityPNG, c.Identity.PNG, 0o600); err != nil {
			return nil, err
		}
	}
	res, err := a.m.Generate(ctx, req)
	if err != nil {
		var un *engine.Unavailable
		if !errors.As(err, &un) {
			un = &engine.Unavailable{Reason: engine.ModelError, Detail: err.Error()}
		}
		return nil, un
	}
	png, err := os.ReadFile(filepath.Join(stage, c.Name+".png"))
	if err != nil {
		return nil, &engine.Unavailable{Reason: engine.ModelError, Detail: fmt.Sprintf("the model tool's PNG is missing: %v", err), CostUSD: res.CostUSD}
	}
	png8x, _ := os.ReadFile(filepath.Join(stage, c.Name+"@8x.png")) // optional
	runID := ""
	if c.Identity != nil {
		runID = c.Identity.RunID // the sprite this avatar was drawn from can be re-derived from its run
	}
	return &engine.Output{HadronRunID: runID, PNG: png, PNG8x: png8x, Model: res.Model, CostUSD: res.CostUSD, PromptSHA256: res.PromptSHA256,
		GoldenSHA256: res.GoldenSHA256, IdentitySHA256: res.IdentitySHA256, Despeckle: res.Despeckle, Extra: res.Sidecar}, nil
}

// Engines wires the standard set: the procedural engine always, and image and
// hybrid when a model tool is configured. Without one those modes are simply
// absent, and the policy's fallback covers them.
func Engines(svg SVGSource, m model.Engine, estimateUSD float64, stageDir string) map[engine.Mode]engine.Engine {
	out := map[engine.Mode]engine.Engine{engine.SVG: NewSVG(svg)}
	if m != nil {
		out[engine.Image] = NewModel(engine.Image, m, estimateUSD, stageDir)
		out[engine.Hybrid] = NewModel(engine.Hybrid, m, estimateUSD, stageDir)
	}
	return out
}
