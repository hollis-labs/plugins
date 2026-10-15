package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/budget"
	gmcpserver "github.com/hollis-labs/libs/mcp/plugin-mcp/go-mcp/server"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/model"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/service"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/spend"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/tools"
)

type fakeGen struct {
	got  service.Request
	resp *service.Response
	err  error
}

func (f *fakeGen) Status(context.Context) service.Status {
	return service.Status{Modes: []service.ModeStatus{{Mode: service.ModeSVG, Available: true}}, Spend: service.SpendStatus{SpentTotal: 0.068, Calls: 2}}
}

func (f *fakeGen) Generate(_ context.Context, r service.Request) (*service.Response, error) {
	f.got = r
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &service.Response{URN: r.URN, RequestedMode: service.ModeHybrid, UsedMode: service.ModeSVG, Files: []service.File{{Path: "/x/a.png", Format: "png", Px: 64, NativeGrid: 8, Role: "canonical"}}}, nil
}

func call(t *testing.T, g tools.Generator, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	srv := gmcpserver.NewServer("avatar-test", "0")
	tools.Register(srv, tools.Deps{Generator: g})
	res, err := srv.CallTool(context.Background(), name, args)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func code(t *testing.T, err error) string {
	t.Helper()
	var te *budget.ToolError
	if !errors.As(err, &te) {
		t.Fatalf("want a ToolError, got %v", err)
	}
	return te.Code
}

func TestArgumentsReachTheService(t *testing.T) {
	g := &fakeGen{}
	_, err := call(t, g, "avatar_generate", map[string]any{
		"urn": "agt-1", "mode": "image", "fallback": "none", "force": true, "role": "builder", "species": "fox", "mood": "calm",
		"palette": "violet", "fill": float64(40), "budget_usd": 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := g.got
	if r.URN != "agt-1" || r.Mode != service.ModeImage || r.Fallback != "none" || !r.Force || r.Role != "builder" || r.Species != "fox" ||
		r.Mood != "calm" || r.Palette != "violet" || r.Fill == nil || *r.Fill != 40 || r.BudgetUSD != 0.5 {
		t.Fatalf("service request: %+v", r)
	}
	// Omitted settings stay unset so the service applies its own defaults.
	g = &fakeGen{}
	call(t, g, "avatar_generate", map[string]any{"urn": "a"})
	if g.got.Fill != nil || g.got.Mode != "" || g.got.Force {
		t.Fatalf("defaults must be left to the service: %+v", g.got)
	}
}

func TestBadArgumentsAreRefusedBeforeTheServiceIsCalled(t *testing.T) {
	cases := map[string]map[string]any{
		"fraction":     {"urn": "a", "fill": 35.5},
		"wrong type":   {"urn": "a", "fill": "lots"},
		"force type":   {"urn": "a", "force": "yes"},
		"budget type":  {"urn": "a", "budget_usd": "cheap"},
		"unknown knob": {"urn": "a", "colour": float64(1)},
	}
	for name, args := range cases {
		g := &fakeGen{}
		_, err := call(t, g, "avatar_generate", args)
		if err == nil || code(t, err) != "invalid_argument" {
			t.Errorf("%s: got %v, want invalid_argument", name, err)
		}
		if g.got.URN != "" {
			t.Errorf("%s: the service was called for an invalid request", name)
		}
	}
}

func TestEveryFailureHasAnActionableCode(t *testing.T) {
	cases := []struct {
		err       error
		want      string
		retryable bool
	}{
		{&avatar.FieldError{Field: "fill", Msg: "bad fill"}, "invalid_argument", false},
		{&service.ModeError{Requested: service.ModeHybrid, Reason: model.CredentialMissing, Detail: "no key"}, "credential_missing", false},
		{&service.ModeError{Requested: service.ModeHybrid, Reason: model.OverBudget, Detail: "x"}, "over_budget", false},
		{&service.ModeError{Requested: service.ModeImage, Reason: model.ScopeMissing, Detail: "x"}, "scope_missing", false},
		{&service.ModeError{Requested: service.ModeImage, Reason: model.ModelError, Detail: "x"}, "model_error", true},
		{&service.ModeError{Requested: service.ModeImage, Reason: model.Unconfigured, Detail: "x"}, "model_unconfigured", false},
		{fmt.Errorf("%w: a_b_c", store.ErrCollision), "name_collision", false},
		{fmt.Errorf("%w: empty", store.ErrInvalidURN), "invalid_argument", false},
		{palette.ErrLocked, "registry_locked", true},
		{fmt.Errorf("%w: x", palette.ErrCorrupt), "registry_corrupt", false},
		{service.ErrBadOutput, "bad_workflow_output", false},
		{fmt.Errorf("%w: x", spend.ErrCorrupt), "spend_ledger_corrupt", false},
		{spend.ErrLocked, "spend_ledger_locked", true},
		{hadron.ErrUnavailable, "hadron_unavailable", true},
		{hadron.ErrTimeout, "hadron_unavailable", true},
		{hadron.ErrConfig, "plugin_misconfigured", false},
		{hadron.ErrServer, "hadron_error", false},
		{hadron.ErrRejected, "workflow_rejected", false},
		{errors.New("surprise"), "internal_error", false},
	}
	for _, c := range cases {
		_, err := call(t, &fakeGen{err: c.err}, "avatar_generate", map[string]any{"urn": "a"})
		var te *budget.ToolError
		if !errors.As(err, &te) || te.Code != c.want {
			t.Errorf("%v: got %v, want code %s", c.err, err, c.want)
			continue
		}
		if te.Retryable != c.retryable {
			t.Errorf("%s: retryable = %v, want %v", c.want, te.Retryable, c.retryable)
		}
	}
}

func TestTheResponseIsTheContractShape(t *testing.T) {
	m, err := call(t, &fakeGen{}, "avatar_generate", map[string]any{"urn": "agt-1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"urn", "requested_mode", "used_mode", "fallback_reason", "existed", "files", "sidecar", "provenance", "reproducible"} {
		if _, ok := m[k]; !ok {
			t.Errorf("response is missing %q: %v", k, m)
		}
	}
	if m["fallback_reason"] != nil {
		t.Errorf("fallback_reason must be null when nothing fell back, got %v", m["fallback_reason"])
	}
}

func TestGuideDescribesTheModesAndFallback(t *testing.T) {
	m, err := call(t, &fakeGen{}, "avatar_guide", nil)
	if err != nil {
		t.Fatal(err)
	}
	g := m["guide"].(string)
	for _, want := range []string{"svg", "image", "hybrid", "fallback", "force", "palette", "stand-in"} {
		if !strings.Contains(g, want) {
			t.Errorf("guide does not mention %q", want)
		}
	}
}

func TestStatusIsReadOnlyAndReportsSpend(t *testing.T) {
	g := &fakeGen{}
	m, err := call(t, g, "avatar_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	sp := m["spend"].(map[string]any)
	if sp["spent_total_usd"].(float64) != 0.068 || sp["calls"].(float64) != 2 || len(m["modes"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	if g.got.URN != "" {
		t.Fatal("status must not generate anything")
	}
}
