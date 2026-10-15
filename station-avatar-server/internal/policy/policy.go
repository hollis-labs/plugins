// Package policy is the avatar service's rules, kept as data: what each mode
// falls back to when it cannot run, and how much paid engines may spend. It is a
// short-term settings surface (a JSON file plus environment overrides); a proper
// settings object replaces it later (Torque CW-20261015-0005).
package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/engine"
)

// MaxChain bounds a mode plus its fallbacks.
const MaxChain = 4

// Spend limits for paid engines, in dollars. Zero means no limit.
type Spend struct {
	PerCallUSD float64 `json:"per_call_usd"`
	DailyUSD   float64 `json:"daily_usd"`
	TotalUSD   float64 `json:"total_usd"`
}

// Policy is the rules.
type Policy struct {
	// Fallback maps a mode to the modes tried, in order, when it cannot run.
	Fallback map[engine.Mode][]engine.Mode `json:"fallback"`
	Spend    Spend                         `json:"spend"`
}

// Default is the house policy: the model modes fall back to the free procedural
// avatar, and nothing is limited.
func Default() Policy {
	return Policy{
		Fallback: map[engine.Mode][]engine.Mode{
			engine.SVG:    nil,
			engine.Image:  {engine.SVG},
			engine.Hybrid: {engine.SVG},
		},
	}
}

// Chain is the modes to try for a request, in order: the mode itself, then its
// fallbacks. With fallback disabled it is the mode alone.
func (p Policy) Chain(m engine.Mode, fallback bool) []engine.Mode {
	chain := []engine.Mode{m}
	if fallback {
		chain = append(chain, p.Fallback[m]...)
	}
	return chain
}

// Ceiling is the per-call spend limit for a request: the configured one, which a
// request's own budget can lower and never raise. Zero means none.
func (s Spend) Ceiling(requested float64) float64 {
	c := s.PerCallUSD
	if requested > 0 && (c == 0 || requested < c) {
		c = requested
	}
	return c
}

// Validate reports the first thing wrong with p.
func (p Policy) Validate() error {
	for mode, chain := range p.Fallback {
		if !mode.Known() {
			return fmt.Errorf("policy: unknown mode %q in fallback", mode)
		}
		seen := map[engine.Mode]bool{mode: true}
		for _, f := range chain {
			if !f.Known() {
				return fmt.Errorf("policy: %s falls back to unknown mode %q", mode, f)
			}
			if seen[f] {
				return fmt.Errorf("policy: %s falls back to %s more than once or to itself", mode, f)
			}
			seen[f] = true
		}
		if len(chain)+1 > MaxChain {
			return fmt.Errorf("policy: %s has %d fallbacks; the limit is %d", mode, len(chain), MaxChain-1)
		}
	}
	for name, v := range map[string]float64{"per_call_usd": p.Spend.PerCallUSD, "daily_usd": p.Spend.DailyUSD, "total_usd": p.Spend.TotalUSD} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("policy: spend.%s must be a non-negative number, got %v", name, v)
		}
	}
	return nil
}

// Load reads a policy file over the defaults: a mode or limit the file does not
// mention keeps its default. Unknown fields are an error, so a misspelt key does
// not silently leave a limit off.
func Load(path string) (Policy, error) {
	p := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return p, fmt.Errorf("policy: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Default(), fmt.Errorf("policy: %s: %w", path, err)
	}
	return p, p.Validate()
}

// Env names. A set variable overrides the file.
const (
	EnvPerCall = "AVATAR_BUDGET_USD"
	EnvDaily   = "AVATAR_DAILY_CAP_USD"
	EnvTotal   = "AVATAR_TOTAL_CAP_USD"
)

// WithEnv applies the spend overrides from the environment.
func (p Policy) WithEnv(get func(string) string) (Policy, error) {
	for env, dst := range map[string]*float64{EnvPerCall: &p.Spend.PerCallUSD, EnvDaily: &p.Spend.DailyUSD, EnvTotal: &p.Spend.TotalUSD} {
		v := strings.TrimSpace(get(env))
		if v == "" {
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return p, fmt.Errorf("policy: %s %q: want a non-negative number", env, v)
		}
		*dst = f
	}
	return p, p.Validate()
}
