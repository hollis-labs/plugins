package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/sprite"
)

// ErrBadOutput: the workflow ran but did not return what this plugin's generator
// version expects, which usually means the deployed workflow file is out of date.
var ErrBadOutput = errors.New("the avatar workflow returned no usable output")

// Runner runs the avatar workflow. *hadron.Client is the real one.
type Runner interface {
	Run(ctx context.Context, runID string, inputs map[string]any) (hadron.Outputs, error)
}

// HadronSVG is the procedural engine: the avatar-generate workflow on hadrond.
type HadronSVG struct{ Runner Runner }

// Generate runs the workflow for r. The run id comes from r, so asking for the
// same sprite twice replays one durable run.
func (h HadronSVG) Generate(ctx context.Context, r avatar.Request) (SVGResult, error) {
	if err := r.Validate(); err != nil {
		return SVGResult{}, err
	}
	out, err := h.Runner.Run(ctx, r.RunID(), map[string]any{"id": r.ID, "fill": r.Knobs.Fill, "hue": r.Knobs.Hue, "accent": r.Accent})
	if err != nil {
		return SVGResult{}, err
	}
	var svg string
	if err := json.Unmarshal(out["svg"], &svg); err != nil || svg == "" {
		return SVGResult{}, ErrBadOutput
	}
	var sprite struct {
		Hue int `json:"hue"`
	}
	if err := json.Unmarshal(out["sprite"], &sprite); err != nil {
		return SVGResult{}, ErrBadOutput
	}
	return SVGResult{SVG: svg, Sprite: out["sprite"], RunID: r.RunID()}, nil
}

// NativeSVG is the procedural engine drawn in-process: the same sprite as the
// workflow, in microseconds, with no Hadron to run or reach.
type NativeSVG struct{}

// Generate draws r. It validates exactly as HadronSVG does.
func (NativeSVG) Generate(_ context.Context, r avatar.Request) (SVGResult, error) {
	if err := r.Validate(); err != nil {
		return SVGResult{}, err
	}
	s := sprite.Draw(r.ID, r.Knobs.Fill, r.Knobs.Hue)
	return SVGResult{SVG: sprite.SVG(s, r.Accent), Sprite: s.JSON(), RunID: r.RunID()}, nil
}
