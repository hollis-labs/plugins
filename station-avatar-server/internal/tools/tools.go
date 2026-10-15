// Package tools is the avatar server's MCP surface: avatar_guide and
// avatar_generate. The descriptions are the documentation agents read, so they
// say what each mode costs and what a call will and will not do.
package tools

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/hollis-labs/go-mcp/budget"
	gmcpserver "github.com/hollis-labs/go-mcp/server"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/model"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/service"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/spend"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

// Generator makes avatars. *service.Service is the real one.
type Generator interface {
	Generate(ctx context.Context, r service.Request) (*service.Response, error)
	Status(ctx context.Context) service.Status
}

// Deps are what the tools call through.
type Deps struct{ Generator Generator }

const guideText = `Avatar generator: 8-bit pixel-robot avatars for agents, stored as files named after the agent.

Call avatar_status to see which modes are available and what has been spent. Call avatar_generate with the agent's URN name. Three modes:
  svg     a procedural sprite from the URN. Free, instant, reproducible: the same URN and settings always give the same face.
  image   a model draws a new character in the house style (a golden reference image). Costs money (about $0.03), not reproducible.
  hybrid  (default) the model is shown the agent's own svg sprite as its identity, so every agent gets a unique face in the house style.

The stored file is the source of truth. If the agent already has an avatar, that file is returned (existed: true) and nothing is generated or spent. Pass force: true for a new face; the old one is kept under a timestamped name.

If the model path cannot run (no model tool, no credential, over budget, provider error), image and hybrid fall back to the svg avatar and say why in fallback_reason. A fallback avatar is a stand-in: asking again for hybrid later replaces it automatically once the model path works. Pass fallback: "none" to get an error instead.

Colours come from a shared table of 16 curated pairs. Each agent is given a slot on first use (palette_registry.json), so the first 16 agents never share one. Override with palette (a slot name) or accent and bg (#rrggbb, both).

Settings: fill 0-100 is how filled the procedural sprite's body is (default 65, integers only). role, species and mood vary the model's drawing and are ignored in svg mode. budget_usd lowers the per-call spend ceiling for this call; it cannot raise the configured one. Operators can also set a daily and a total limit, and change what each mode falls back to; avatar_status shows both.

Files: <name>.png is canonical (64x64), <name>@8x.png is 512x512, <name>.svg is the vector (svg avatars only), <name>.json is the sidecar with provenance. Generator: ` + avatar.GeneratorVersion + `.`

// Register adds the avatar tools to srv.
func Register(srv *gmcpserver.Server, d Deps) {
	srv.RegisterTool(gmcpserver.Tool{
		Name:         "avatar_guide",
		Description:  "How the avatar generator works: the three modes, what each costs, how fallback and palettes work. Read this before avatar_generate.",
		InputSchema:  gmcpserver.InputSchema(),
		ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false,
		Handler: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"guide": guideText, "generator": avatar.GeneratorVersion, "default_fill": avatar.FillDefault}, nil
		},
	})

	srv.RegisterTool(gmcpserver.Tool{
		Name: "avatar_status",
		Description: "Which avatar modes are available right now and why not if one is not, what each costs, what each falls back to, and how much has been spent against the limits. " +
			"Read-only; never makes a paid call. Check this before asking for a model mode if cost matters.",
		InputSchema:  gmcpserver.InputSchema(),
		ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) { return d.Generator.Status(ctx), nil },
	})

	props := []gmcpserver.Prop{
		gmcpserver.StringProp("urn", "The agent's URN name; it is also the file name. The same URN always maps to the same files.", true),
		gmcpserver.StringEnumProp("mode", "svg (free, reproducible), image (model, house style) or hybrid (model, shown the agent's own sprite). Default hybrid.", false, "svg", "image", "hybrid"),
		gmcpserver.StringEnumProp("fallback", `What to do if the model path cannot run: "svg" (default) delivers the svg avatar and records why; "none" returns an error.`, false, "svg", "none"),
		gmcpserver.BooleanProp("force", "Generate a new face even if the agent has one. The old files are kept under a timestamped name.", false),
		gmcpserver.StringProp("role", "What the agent does, e.g. builder. Varies the model's drawing; ignored in svg mode.", false),
		gmcpserver.StringProp("species", "What kind of character to draw. Default robot. Ignored in svg mode.", false),
		gmcpserver.StringProp("mood", "A mood for the model to draw, e.g. calm. Ignored in svg mode.", false),
		gmcpserver.StringProp("palette", "A slot name from the shared colour table (e.g. violet) instead of the agent's registry slot. Give this or accent and bg, not both.", false),
		gmcpserver.StringProp("accent", "Explicit body colour, #rrggbb. Must be given with bg.", false),
		gmcpserver.StringProp("bg", "Explicit background colour, #rrggbb. Must be given with accent.", false),
		gmcpserver.IntegerProp("fill", fmt.Sprintf("Body fill of the svg sprite in percent, %d-%d. Default %d.", avatar.FillMin, avatar.FillMax, avatar.FillDefault), false),
		{Name: "budget_usd", Schema: map[string]interface{}{"type": "number", "minimum": 0, "description": "Spend ceiling in dollars for this call. It can lower the configured ceiling, never raise it."}},
	}
	srv.RegisterTool(gmcpserver.Tool{
		Name: "avatar_generate",
		Description: "Generate (or return) the avatar for an agent URN. Idempotent: an agent that already has an avatar gets it back, free, unless force is set. " +
			"Modes image and hybrid call a paid model and fall back to the free svg avatar when they cannot run; the response says which mode was used and why. " +
			"Writes files to the avatar store. See avatar_guide.",
		InputSchema: gmcpserver.InputSchema(props...),
		// It writes files, never deletes (forced regenerates archive), and the same call twice is the same files.
		ReadOnlyHint: false, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false,
		Handler: guarded(props, d.generate),
	})
}

// Names lists the registered tool names.
func Names() []string { return []string{"avatar_guide", "avatar_status", "avatar_generate"} }

func (d Deps) generate(ctx context.Context, args map[string]any) (any, error) {
	r := service.Request{
		URN: str(args, "urn"), Mode: service.Mode(str(args, "mode")), Fallback: str(args, "fallback"),
		Role: str(args, "role"), Species: str(args, "species"), Mood: str(args, "mood"),
		Palette: str(args, "palette"), Accent: str(args, "accent"), BG: str(args, "bg"),
	}
	var err error
	if r.Force, err = boolean(args, "force"); err != nil {
		return nil, err
	}
	if v, ok, err := integer(args, "fill"); err != nil {
		return nil, err
	} else if ok {
		r.Fill = &v
	}
	if r.BudgetUSD, err = number(args, "budget_usd"); err != nil {
		return nil, err
	}
	resp, err := d.Generator.Generate(ctx, r)
	if err != nil {
		return nil, fail(err, r)
	}
	return resp, nil
}

// fail maps every way a request can fail onto a tool error whose code a caller
// can act on, and says whether retrying could help.
func fail(err error, r service.Request) error {
	var fe *avatar.FieldError
	var me *service.ModeError
	switch {
	case errors.As(err, &fe):
		return invalid(fe.Field, fe.Msg)
	case errors.As(err, &me):
		te := budget.NewToolError(string(me.Reason), me.Error()).WithHelpTool("avatar_guide").
			WithNextStep("Retry with fallback \"svg\" (the default) for a free avatar, or fix the cause named above.")
		return te.WithRetryable(me.Reason == model.ModelError)
	case errors.Is(err, spend.ErrCorrupt):
		return budget.NewToolError("spend_ledger_corrupt", err.Error()).
			WithNextStep("Not retryable. A spend limit is set but the ledger that backs it is unreadable, so no paid call was made. Restore or inspect spend_ledger.json; it was left untouched.")
	case errors.Is(err, spend.ErrLocked):
		return budget.NewToolError("spend_ledger_locked", err.Error()).WithRetryable(true)
	case errors.Is(err, store.ErrCollision):
		return budget.NewToolError("name_collision", err.Error()).WithField("urn").
			WithNextStep("Two different URNs sanitise to the same file name. Use distinct URNs; the existing avatar was not touched.")
	case errors.Is(err, store.ErrInvalidURN):
		return invalid("urn", err.Error())
	case errors.Is(err, palette.ErrLocked):
		return budget.NewToolError("registry_locked", err.Error()).WithRetryable(true)
	case errors.Is(err, palette.ErrCorrupt):
		return budget.NewToolError("registry_corrupt", err.Error()).
			WithNextStep("Not retryable. The palette registry file is unreadable and was left untouched; restore it from backup or inspect it. Overwriting it would reassign every agent's colours.")
	case errors.Is(err, service.ErrBadOutput):
		return budget.NewToolError("bad_workflow_output", err.Error()+"; the deployed workflow may not match this plugin's generator version "+avatar.GeneratorVersion)
	case errors.Is(err, hadron.ErrUnavailable), errors.Is(err, hadron.ErrTimeout):
		return budget.NewToolError("hadron_unavailable", err.Error()).WithRetryable(true).
			WithNextStep("Check that hadrond is running and ready (`hadron daemon`), then retry.")
	case errors.Is(err, hadron.ErrConfig):
		return budget.NewToolError("plugin_misconfigured", err.Error()).
			WithNextStep("Not retryable. Check HADRON_TOKEN_FILE (default ~/.hadron/operator.token) in the avatar server's environment.")
	case errors.Is(err, hadron.ErrServer):
		return budget.NewToolError("hadron_error", err.Error()).
			WithNextStep("Not retryable as is. Check AVATAR_WORKFLOW points at the deployed avatar-generate workflow under hadrond's workflow source root.")
	case errors.Is(err, hadron.ErrRejected):
		return budget.NewToolError("workflow_rejected", err.Error()).
			WithNextStep("Hadron refused the run. If the message names a definition or source root, the workflow file is not deployed under hadrond's workflow source root.")
	case errors.Is(err, hadron.ErrRunFailed), errors.Is(err, hadron.ErrConflict):
		return budget.NewToolError("hadron_error", err.Error())
	default:
		return budget.NewToolError("internal_error", err.Error())
	}
}

// guarded rejects an argument the tool does not take: a misspelt setting would
// otherwise be silently ignored and the caller would get the default avatar.
func guarded(props []gmcpserver.Prop, run func(context.Context, map[string]any) (any, error)) gmcpserver.ToolHandler {
	allowed := map[string]bool{}
	names := ""
	for i, p := range props {
		allowed[p.Name] = true
		if i > 0 {
			names += ", "
		}
		names += p.Name
	}
	return func(ctx context.Context, args map[string]any) (any, error) {
		for k := range args {
			if !allowed[k] {
				return nil, invalid(k, fmt.Sprintf("avatar_generate takes no argument %q; it accepts %s", k, names))
			}
		}
		return run(ctx, args)
	}
}

func invalid(field, msg string) error {
	return budget.NewToolError("invalid_argument", msg).WithField(field).WithHelpTool("avatar_guide")
}

func str(a map[string]any, k string) string {
	s, _ := a[k].(string)
	return s
}

func boolean(a map[string]any, k string) (bool, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return false, nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, invalid(k, fmt.Sprintf("%s must be true or false, got %T", k, v))
	}
	return b, nil
}

// integer reads an optional integer. MCP delivers numbers as float64, so a
// fractional value is refused rather than truncated: the procedural generator
// takes integers only, and 35.5 silently becoming 35 hides the caller's mistake.
func integer(a map[string]any, k string) (int, bool, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return 0, false, nil
	}
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) || math.IsNaN(n) {
			return 0, false, invalid(k, fmt.Sprintf("%s must be an integer, got %v", k, n))
		}
		return int(n), true, nil
	case int:
		return n, true, nil
	default:
		return 0, false, invalid(k, fmt.Sprintf("%s must be an integer, got %T", k, v))
	}
}

func number(a map[string]any, k string) (float64, error) {
	v, ok := a[k]
	if !ok || v == nil {
		return 0, nil
	}
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	default:
		return 0, invalid(k, fmt.Sprintf("%s must be a number, got %T", k, v))
	}
}
