// Package engine is the seam between the avatar service and whatever draws an
// avatar. An engine is an adapter that declares its capabilities (does it cost
// money, can it be reproduced, does it want the agent's own sprite as input), so
// the service decides what to run, what it may spend and what to fall back to
// from those declarations and a policy, never from knowing which engine it is.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

// Mode names an engine.
type Mode string

const (
	SVG    Mode = "svg"
	Image  Mode = "image"
	Hybrid Mode = "hybrid"
)

// Known reports whether m is a mode this package defines.
func (m Mode) Known() bool { return m == SVG || m == Image || m == Hybrid }

// Reason is why an engine could not produce an avatar. These are the contract's
// fallback_reason values.
type Reason string

const (
	CredentialMissing Reason = "credential_missing"
	ScopeMissing      Reason = "scope_missing"
	OverBudget        Reason = "over_budget"
	ModelError        Reason = "model_error"
	Unconfigured      Reason = "model_unconfigured"
)

// Unavailable is an engine declining or failing in a way a fallback policy may
// cover: the engine could not run, or ran and did not produce an avatar. Any
// other error from an engine is infrastructure the caller must see.
type Unavailable struct {
	Reason Reason
	Detail string
	// CostUSD is what a failed attempt still cost (a provider can bill for a
	// response with no usable image); it is recorded against the spend limits.
	CostUSD float64
}

func (e *Unavailable) Error() string { return string(e.Reason) + ": " + e.Detail }

// Capabilities describe an engine without naming it.
type Capabilities struct {
	// Paid engines cost money and are subject to spend limits.
	Paid bool
	// EstimateUSD is the expected cost of one call, used to gate a call before it
	// is made. Zero for free engines.
	EstimateUSD float64
	// Reproducible: the same inputs always give the same bytes.
	Reproducible bool
	// NeedsIdentity: the engine wants the agent's procedural sprite as input.
	NeedsIdentity bool
	// ProvidesIdentity: the engine can draw that sprite.
	ProvidesIdentity bool
}

// Identity is an agent's procedural sprite, offered to engines that want one.
type Identity struct {
	SVG   string
	PNG   []byte // 512px, transparent background: the silhouette is read from alpha
	RunID string
}

// Call is one avatar to draw.
type Call struct {
	URN, Name           string // Name is the file stem
	Role, Species, Mood string
	// Accent and BG are #RRGGBB; Slot names the palette slot they came from.
	Accent, BG, Slot string
	Fill             int
	// Identity is set for engines that need it, and may be passed to others that
	// can reuse it.
	Identity *Identity
}

// Output is a drawn avatar.
type Output struct {
	PNG, PNG8x []byte
	SVG        string // only for vector engines; already on its background
	Colors     []string

	Model          string
	CostUSD        float64
	PromptSHA256   string
	GoldenSHA256   string
	IdentitySHA256 string
	Despeckle      bool
	HadronRunID    string
	// Extra is the adapter's own sidecar fields, stored verbatim beside ours.
	Extra map[string]json.RawMessage
}

// Engine draws avatars.
type Engine interface {
	Mode() Mode
	Capabilities() Capabilities
	// Preflight reports why the engine cannot run right now, without running it.
	Preflight() *Unavailable
	// Generate draws one avatar. An *Unavailable error is a failure a fallback
	// policy may cover; any other error is returned to the caller as is.
	Generate(ctx context.Context, c Call) (*Output, error)
}

// IdentityProvider is implemented by engines that can draw the agent's sprite
// for another engine to use.
type IdentityProvider interface {
	Identity(ctx context.Context, c Call) (*Identity, error)
}

// String makes modes read well in messages.
func (m Mode) String() string { return string(m) }

// Describe is a one-line summary for status output.
func (c Capabilities) Describe() string {
	return fmt.Sprintf("paid=%v reproducible=%v needs_identity=%v provides_identity=%v estimate_usd=%.3f",
		c.Paid, c.Reproducible, c.NeedsIdentity, c.ProvidesIdentity, c.EstimateUSD)
}
