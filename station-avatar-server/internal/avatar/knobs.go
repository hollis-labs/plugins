// Package avatar is the front door's view of the pixel-robot avatar generator:
// the knobs a caller may turn, their defaults and ranges, and the identity of a
// generation. The generator itself is a Hadron workflow (see
// plugins/avatar-server/workflows); this package validates before anything
// reaches it, so a bad value is a clear tool error rather than a failed run.
package avatar

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// GeneratorVersion names the generator a run was produced by. It is part of a
// run's identity, so changing the workflow's behaviour means bumping it:
// otherwise Hadron would replay the old run for the same inputs.
const GeneratorVersion = "v2"

// Knob bounds and defaults. They mirror the workflow's input schema; the
// workflow enforces them too, this side exists to give a better error.
const (
	MaxIDLen = 128

	FillMin, FillMax, FillDefault = 0, 100, 65
	// HueSeeded asks for the hue derived from the id.
	HueMin, HueMax, HueDefault = -1, 359, -1
)

// Knobs are the tweakable parameters. Integers only: Hadron's script step
// rejects decimals that are not exactly representable as a float64.
type Knobs struct {
	// Fill is how filled the body is, in percent. 65 is the house style.
	Fill int `json:"fill"`
	// Hue overrides the seeded hue, in degrees; -1 keeps the seeded one.
	Hue int `json:"hue"`
}

// Defaults are the house-style knobs.
func Defaults() Knobs { return Knobs{Fill: FillDefault, Hue: HueDefault} }

// Request is one avatar to generate.
type Request struct {
	// ID is the agent's stable identity; the same ID always gets the same face.
	ID    string
	Knobs Knobs
	// Accent is the body colour as #rrggbb. Empty keeps the colour derived from
	// Knobs.Hue (the v1 behaviour).
	Accent string
}

var hexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// ValidHex reports whether s is #rrggbb.
func ValidHex(s string) bool { return hexColor.MatchString(s) }

// Validate reports the first thing wrong with r, naming the field.
func (r Request) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return &FieldError{Field: "id", Msg: "id is required"}
	}
	if len(r.ID) > MaxIDLen {
		return &FieldError{Field: "id", Msg: fmt.Sprintf("id is %d bytes; the limit is %d", len(r.ID), MaxIDLen)}
	}
	if r.Knobs.Fill < FillMin || r.Knobs.Fill > FillMax {
		return &FieldError{Field: "fill", Msg: fmt.Sprintf("fill must be an integer from %d to %d, got %d", FillMin, FillMax, r.Knobs.Fill)}
	}
	if r.Knobs.Hue < HueMin || r.Knobs.Hue > HueMax {
		return &FieldError{Field: "hue", Msg: fmt.Sprintf("hue must be an integer from %d to %d (-1 keeps the seeded hue), got %d", HueMin, HueMax, r.Knobs.Hue)}
	}
	if r.Accent != "" && !ValidHex(r.Accent) {
		return &FieldError{Field: "accent", Msg: fmt.Sprintf("accent must be #rrggbb, got %q", r.Accent)}
	}
	return nil
}

// RunID is the Hadron run id for r. It is derived from everything that
// determines the output, so asking for the same avatar twice is a replay of the
// same durable run, not a second one.
func (r Request) RunID() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d|%s", GeneratorVersion, r.ID, r.Knobs.Fill, r.Knobs.Hue, strings.ToLower(r.Accent))))
	return "avatar-" + GeneratorVersion + "-" + hex.EncodeToString(sum[:8])
}

// FieldError is a validation failure that names the field at fault.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Msg }
