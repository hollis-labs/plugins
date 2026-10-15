package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/station-plugins/avatar-server/internal/engine"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultFallsBackToTheFreeModeAndLimitsNothing(t *testing.T) {
	p := Default()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Chain(engine.Hybrid, true), []engine.Mode{engine.Hybrid, engine.SVG}) {
		t.Errorf("hybrid chain = %v", p.Chain(engine.Hybrid, true))
	}
	if !reflect.DeepEqual(p.Chain(engine.Image, true), []engine.Mode{engine.Image, engine.SVG}) {
		t.Errorf("image chain = %v", p.Chain(engine.Image, true))
	}
	if got := p.Chain(engine.SVG, true); len(got) != 1 {
		t.Errorf("svg has nothing to fall back to: %v", got)
	}
	if p.Spend != (Spend{}) {
		t.Errorf("default spend = %+v", p.Spend)
	}
}

func TestChainWithFallbackDisabledIsTheModeAlone(t *testing.T) {
	if got := Default().Chain(engine.Hybrid, false); !reflect.DeepEqual(got, []engine.Mode{engine.Hybrid}) {
		t.Fatalf("got %v", got)
	}
}

func TestAFileOverridesOnlyWhatItMentions(t *testing.T) {
	p, err := Load(write(t, `{"fallback":{"hybrid":["image","svg"]},"spend":{"daily_usd":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Chain(engine.Hybrid, true), []engine.Mode{engine.Hybrid, engine.Image, engine.SVG}) {
		t.Errorf("hybrid = %v", p.Chain(engine.Hybrid, true))
	}
	if !reflect.DeepEqual(p.Chain(engine.Image, true), []engine.Mode{engine.Image, engine.SVG}) {
		t.Errorf("image must keep its default: %v", p.Chain(engine.Image, true))
	}
	if p.Spend.DailyUSD != 2 || p.Spend.TotalUSD != 0 {
		t.Errorf("spend = %+v", p.Spend)
	}
}

func TestAMisspeltKeyIsAnErrorNotASilentlyMissingLimit(t *testing.T) {
	for _, body := range []string{`{"spend":{"dayly_usd":2}}`, `{"fallbacks":{}}`, `{"spend":{"daily_usd":"two"}}`, `not json`} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want an error", body)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("a missing file named on purpose is an error")
	}
}

func TestValidationRefusesNonsense(t *testing.T) {
	cases := map[string]string{
		"unknown mode":      `{"fallback":{"photo":["svg"]}}`,
		"unknown fallback":  `{"fallback":{"hybrid":["photo"]}}`,
		"self":              `{"fallback":{"hybrid":["hybrid"]}}`,
		"duplicate":         `{"fallback":{"hybrid":["svg","svg"]}}`,
		"negative per call": `{"spend":{"per_call_usd":-1}}`,
		"negative total":    `{"spend":{"total_usd":-0.01}}`,
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

func TestEnvOverridesTheFile(t *testing.T) {
	base, _ := Load(write(t, `{"spend":{"per_call_usd":0.5,"daily_usd":2}}`))
	env := map[string]string{EnvPerCall: "0.1", EnvTotal: "7.5"}
	p, err := base.WithEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if p.Spend != (Spend{PerCallUSD: 0.1, DailyUSD: 2, TotalUSD: 7.5}) {
		t.Fatalf("spend = %+v", p.Spend)
	}
	for _, bad := range []string{"abc", "-1", "NaN", "Inf"} {
		if _, err := base.WithEnv(func(k string) string {
			if k == EnvDaily {
				return bad
			}
			return ""
		}); err == nil || !strings.Contains(err.Error(), EnvDaily) {
			t.Errorf("%q: got %v", bad, err)
		}
	}
}

func TestARequestCanLowerThePerCallCeilingNeverRaiseIt(t *testing.T) {
	for _, c := range []struct{ cfg, req, want float64 }{
		{0, 0, 0}, {0.5, 0, 0.5}, {0.5, 0.1, 0.1}, {0.1, 5, 0.1}, {0, 0.2, 0.2},
	} {
		if got := (Spend{PerCallUSD: c.cfg}).Ceiling(c.req); got != c.want {
			t.Errorf("config %v request %v: ceiling %v, want %v", c.cfg, c.req, got, c.want)
		}
	}
}
