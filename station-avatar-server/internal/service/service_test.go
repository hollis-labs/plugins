package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/hollis-labs/station-plugins/avatar-server/internal/avatar"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/engine"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/model"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/palette"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/policy"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/spend"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/store"
)

// A 3-pixel sprite in the shape the workflow emits.
const testSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" width="64" height="64" shape-rendering="crispEdges">` +
	`<rect x="1" y="0" width="1" height="1" fill="#ff6b5b"/><rect x="2" y="5" width="1" height="1" fill="#994037"/>` +
	`<rect x="3" y="2" width="1" height="1" fill="#a7f3d0"/></svg>`

type fakeSVG struct {
	calls atomic.Int32
	last  avatar.Request
	err   error
}

func (f *fakeSVG) Generate(_ context.Context, r avatar.Request) (SVGResult, error) {
	f.calls.Add(1)
	f.last = r
	if f.err != nil {
		return SVGResult{}, f.err
	}
	return SVGResult{SVG: testSVG, Sprite: json.RawMessage(`{"hue":10}`), RunID: r.RunID()}, nil
}

type fakeModel struct {
	pre      *model.Error
	genErr   error
	calls    atomic.Int32
	last     model.Request
	identity []byte
}

func (f *fakeModel) Preflight(float64) *model.Error { return f.pre }

func (f *fakeModel) Generate(_ context.Context, r model.Request) (*model.Result, error) {
	f.calls.Add(1)
	f.last = r
	if r.IdentityPNG != "" {
		f.identity, _ = os.ReadFile(r.IdentityPNG)
	}
	if f.genErr != nil {
		return nil, f.genErr
	}
	_ = os.WriteFile(filepath.Join(r.OutDir, r.Name+".png"), []byte("MODEL64"), 0o644)
	_ = os.WriteFile(filepath.Join(r.OutDir, r.Name+"@8x.png"), []byte("MODEL512"), 0o644)
	return &model.Result{Model: "fake/m", CostUSD: 0.034, PromptSHA256: "p", GoldenSHA256: "g", IdentitySHA256: "i", Despeckle: true,
		Sidecar: map[string]json.RawMessage{"model": json.RawMessage(`"fake/m"`), "colors_used": json.RawMessage(`9`)}}, nil
}

type fixture struct {
	s   *Service
	svg *fakeSVG
	mod *fakeModel
	dir string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{svg: &fakeSVG{}, mod: &fakeModel{}, dir: dir}
	f.s = &Service{
		Store: &store.Store{Dir: dir}, Registry: &palette.Registry{Path: filepath.Join(dir, "palette_registry.json")},
		Engines: Engines(f.svg, f.mod, 0.034, dir), Policy: policy.Default(), Ledger: &spend.Ledger{Path: filepath.Join(dir, "spend_ledger.json")},
		Now: func() time.Time { return time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC) },
	}
	return f
}

func gen(t *testing.T, f *fixture, r Request) *Response {
	t.Helper()
	resp, err := f.s.Generate(context.Background(), r)
	if err != nil {
		t.Fatalf("Generate(%+v): %v", r, err)
	}
	return resp
}

func pixel(t *testing.T, path string, x, y int) color.NRGBA {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
}

func code(err error) string {
	var me *ModeError
	if errors.As(err, &me) {
		return string(me.Reason)
	}
	return ""
}

func TestSVGModeStoresPNGsAndSVGOnTheSlotsBackground(t *testing.T) {
	f := setup(t)
	resp := gen(t, f, Request{URN: "agt-cogsworth", Mode: ModeSVG})
	if resp.UsedMode != ModeSVG || resp.FallbackReason != nil || resp.Existed || !resp.Reproducible {
		t.Fatalf("response: %+v", resp)
	}
	if f.mod.calls.Load() != 0 {
		t.Fatal("svg mode must never touch the model")
	}
	if len(resp.Files) != 3 {
		t.Fatalf("want png, 8x png and svg, got %+v", resp.Files)
	}
	by := map[string]File{}
	for _, fl := range resp.Files {
		by[fl.Role] = fl
	}
	if by["canonical"].Px != 64 || by["canonical"].NativeGrid != 8 || by["upscaled"].Px != 512 || by["vector"].Format != "svg" {
		t.Fatalf("files: %+v", resp.Files)
	}
	// Slot colours were applied: the accent went to the engine and the BG is behind the sprite.
	pal := resp.Provenance.Palette
	if f.svg.last.Accent != pal.Accent || pal.Accent == "" || pal.Slot == "" {
		t.Fatalf("palette: %+v, engine got accent %q", pal, f.svg.last.Accent)
	}
	if c := pixel(t, by["canonical"].Path, 63, 63); c != hexRGBA(pal.BG) {
		t.Errorf("background pixel = %v, want %s", c, pal.BG)
	}
	if c := pixel(t, by["canonical"].Path, 8, 0); c != (color.NRGBA{0xff, 0x6b, 0x5b, 255}) {
		t.Errorf("sprite pixel = %v", c)
	}
	svg, _ := os.ReadFile(by["vector"].Path)
	if !strings.Contains(string(svg), `fill="`+strings.ToLower(pal.BG)+`"`) {
		t.Errorf("svg has no background rect:\n%s", svg)
	}
	if resp.Provenance.HadronRunID == "" || resp.Provenance.GeneratorVersion != avatar.GeneratorVersion {
		t.Errorf("provenance: %+v", resp.Provenance)
	}
}

func hexRGBA(h string) color.NRGBA {
	v, err := strconv.ParseUint(strings.TrimPrefix(h, "#"), 16, 32)
	if err != nil {
		panic(err)
	}
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

func TestAskingAgainReturnsTheStoredFileAndSpendsNothing(t *testing.T) {
	f := setup(t)
	gen(t, f, Request{URN: "agt-1", Mode: ModeHybrid})
	svgCalls, modelCalls := f.svg.calls.Load(), f.mod.calls.Load()
	again := gen(t, f, Request{URN: "agt-1", Mode: ModeHybrid})
	if !again.Existed || f.svg.calls.Load() != svgCalls || f.mod.calls.Load() != modelCalls {
		t.Fatalf("second call must be free: existed=%v svg %d->%d model %d->%d", again.Existed, svgCalls, f.svg.calls.Load(), modelCalls, f.mod.calls.Load())
	}
	if again.UsedMode != ModeHybrid || again.Provenance.Model != "fake/m" {
		t.Fatalf("existing response must carry the stored provenance: %+v", again)
	}
}

func TestHybridShowsTheModelTheAgentsOwnSprite(t *testing.T) {
	f := setup(t)
	resp := gen(t, f, Request{URN: "agt-1", Role: "builder", Mood: "calm"}) // default mode: hybrid
	if resp.UsedMode != ModeHybrid || resp.Reproducible || resp.FallbackReason != nil {
		t.Fatalf("response: %+v", resp)
	}
	if f.svg.calls.Load() != 1 || f.mod.calls.Load() != 1 {
		t.Fatalf("hybrid = one sprite + one model call, got %d, %d", f.svg.calls.Load(), f.mod.calls.Load())
	}
	m := f.mod.last
	if m.IdentityPNG == "" || m.Role != "builder" || m.Mood != "calm" || m.Species != "robot" || m.Accent != resp.Provenance.Palette.Accent {
		t.Fatalf("model request: %+v", m)
	}
	// The identity reference is the sprite at 512px on a transparent background:
	// the model tool reads the silhouette from alpha.
	img, err := png.Decode(bytes.NewReader(f.mod.identity))
	if err != nil || img.Bounds().Dx() != 512 {
		t.Fatalf("identity ref: %v %v", err, img)
	}
	if _, _, _, a := img.At(511, 511).RGBA(); a != 0 {
		t.Fatal("identity reference must be transparent outside the sprite")
	}
	if b, _ := os.ReadFile(resp.Files[0].Path); string(b) != "MODEL64" {
		t.Fatalf("canonical file is not the model's: %q", b)
	}
	p := resp.Provenance
	if p.Model != "fake/m" || p.CostUSD != 0.034 || p.IdentityRefSHA != "i" || p.GoldenRefSHA256 != "g" || p.HadronRunID == "" || !p.Despeckle {
		t.Fatalf("provenance: %+v", p)
	}
	// The model tool's own sidecar fields survive next to ours.
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(resp.Sidecar)
	json.Unmarshal(b, &raw)
	if string(raw["colors_used"]) != "9" || raw["avatar"] == nil {
		t.Fatalf("sidecar: %s", b)
	}
	// Nothing is left behind in the store but the avatar's own files.
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stage-") {
			t.Fatalf("staging directory leaked: %s", e.Name())
		}
	}
}

func TestImageModeSendsNoIdentityAndSkipsTheSprite(t *testing.T) {
	f := setup(t)
	resp := gen(t, f, Request{URN: "agt-1", Mode: ModeImage})
	if resp.UsedMode != ModeImage || f.svg.calls.Load() != 0 || f.mod.last.IdentityPNG != "" {
		t.Fatalf("image mode: used=%s svg=%d identity=%q", resp.UsedMode, f.svg.calls.Load(), f.mod.last.IdentityPNG)
	}
}

func TestEveryReasonFallsBackToSVGAndRecordsWhy(t *testing.T) {
	cases := map[string]func(*fixture){
		"model_unconfigured": func(f *fixture) { f.s.Engines = Engines(f.svg, nil, 0.034, f.dir) },
		"credential_missing": func(f *fixture) { f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"} },
		"scope_missing":      func(f *fixture) { f.mod.pre = &model.Error{Reason: model.ScopeMissing, Detail: "scope"} },
		"over_budget":        func(f *fixture) { f.s.Policy.Spend.PerCallUSD = 0.01 },
		"model_error":        func(f *fixture) { f.mod.genErr = &model.Error{Reason: model.ModelError, Detail: "boom"} },
	}
	for want, arrange := range cases {
		for _, mode := range []Mode{ModeHybrid, ModeImage} {
			f := setup(t)
			arrange(f)
			resp := gen(t, f, Request{URN: "agt-1", Mode: mode})
			if resp.UsedMode != ModeSVG || resp.FallbackReason == nil || *resp.FallbackReason != want || !resp.Reproducible {
				t.Errorf("%s/%s: %+v", mode, want, resp)
			}
			if resp.RequestedMode != mode || resp.Provenance.RequestedMode != string(mode) || resp.Provenance.FallbackReason != want {
				t.Errorf("%s/%s: the fallback must be recorded: %+v", mode, want, resp.Provenance)
			}
			if len(resp.Files) != 3 {
				t.Errorf("%s/%s: fallback must still deliver files: %+v", mode, want, resp.Files)
			}
		}
	}
}

func TestAnUnexpectedModelFailureIsStillAModelError(t *testing.T) {
	f := setup(t)
	f.mod.genErr = errors.New("something odd")
	resp := gen(t, f, Request{URN: "agt-1"})
	if resp.FallbackReason == nil || *resp.FallbackReason != "model_error" || !strings.Contains(resp.FallbackDetail, "something odd") {
		t.Fatalf("%+v", resp)
	}
}

func TestFallbackNoneFailsInsteadAndWritesNothing(t *testing.T) {
	f := setup(t)
	f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"}
	_, err := f.s.Generate(context.Background(), Request{URN: "agt-1", Mode: ModeHybrid, Fallback: "none"})
	if code(err) != "credential_missing" {
		t.Fatalf("got %v", err)
	}
	if f.s.Store.Exists("agt-1") {
		t.Fatal("a refused request must not leave a file")
	}
	f2 := setup(t)
	f2.mod.genErr = &model.Error{Reason: model.ModelError, Detail: "boom"}
	if _, err := f2.s.Generate(context.Background(), Request{URN: "agt-1", Fallback: "none"}); code(err) != "model_error" || f2.s.Store.Exists("agt-1") {
		t.Fatalf("call-time failure with fallback none: %v", err)
	}
}

func TestBudgetCeilingIsTheLowerOfConfigAndRequest(t *testing.T) {
	cases := []struct {
		name       string
		cfg, req   float64
		wantPaid   bool
		wantReason string
	}{
		{"no limit anywhere", 0, 0, true, ""},
		{"config allows it", 0.50, 0, true, ""},
		{"a request can lower the ceiling", 0.50, 0.01, false, "over_budget"},
		{"a request cannot raise the configured ceiling", 0.01, 5, false, "over_budget"},
		{"a request alone can set one", 0, 0.01, false, "over_budget"},
		{"a request above the estimate is fine", 0, 0.10, true, ""},
	}
	for _, c := range cases {
		f := setup(t)
		f.s.Policy.Spend.PerCallUSD = c.cfg
		resp := gen(t, f, Request{URN: "a", Mode: ModeImage, BudgetUSD: c.req})
		paid := resp.UsedMode == ModeImage
		if paid != c.wantPaid || (c.wantReason != "" && (resp.FallbackReason == nil || *resp.FallbackReason != c.wantReason)) {
			t.Errorf("%s: used=%s reason=%v", c.name, resp.UsedMode, resp.FallbackReason)
		}
		if !paid && f.mod.calls.Load() != 0 {
			t.Errorf("%s: the model was called despite the ceiling", c.name)
		}
	}
}

func TestDailyAndTotalLimitsGateOnTheLedger(t *testing.T) {
	f := setup(t)
	f.s.Policy.Spend = policy.Spend{DailyUSD: 0.10, TotalUSD: 0.20}
	var modes []Mode
	for _, urn := range []string{"a", "b", "c", "d"} {
		modes = append(modes, gen(t, f, Request{URN: urn, Mode: ModeImage}).UsedMode)
	}
	// $0.034 each: three fit under $0.10 a day (0.102 > 0.10 on the third).
	if got := modes; got[0] != ModeImage || got[1] != ModeImage || got[2] != ModeSVG || got[3] != ModeSVG {
		t.Fatalf("modes = %v", got)
	}
	if f.mod.calls.Load() != 2 {
		t.Fatalf("model calls = %d, want 2", f.mod.calls.Load())
	}
	// Tomorrow the daily limit resets; the total limit (0.20) still has room for three more.
	f.s.Now = func() time.Time { return time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC) }
	if got := gen(t, f, Request{URN: "e", Mode: ModeImage}).UsedMode; got != ModeImage {
		t.Fatalf("the daily limit must reset at the UTC day boundary, got %s", got)
	}
	// The total limit does not reset: push it over.
	f.s.Policy.Spend.TotalUSD = 0.10
	resp := gen(t, f, Request{URN: "f", Mode: ModeImage})
	if resp.UsedMode != ModeSVG || resp.FallbackReason == nil || !strings.Contains(resp.FallbackDetail, "total limit") {
		t.Fatalf("%+v", resp)
	}
}

func TestEveryPaidAttemptIsRecordedIncludingFailedOnes(t *testing.T) {
	f := setup(t)
	gen(t, f, Request{URN: "ok", Mode: ModeImage})
	f.mod.genErr = &engine.Unavailable{Reason: engine.ModelError, Detail: "no image", CostUSD: 0.034}
	gen(t, f, Request{URN: "billed-for-nothing", Mode: ModeImage}) // falls back to svg, but the provider billed
	gen(t, f, Request{URN: "free", Mode: ModeSVG})                 // free engines are not paid attempts
	tot, err := f.s.Ledger.Totals(f.s.now())
	if err != nil {
		t.Fatal(err)
	}
	if tot.Calls != 2 || tot.All != 0.068 {
		t.Fatalf("a failed attempt that cost money must still count against the limits: %+v", tot)
	}
	// An attempt refused before it was made costs nothing and is not recorded.
	f.s.Policy.Spend.PerCallUSD = 0.001
	gen(t, f, Request{URN: "refused", Mode: ModeImage})
	if tot, _ := f.s.Ledger.Totals(f.s.now()); tot.Calls != 2 {
		t.Fatalf("a refused attempt was recorded: %+v", tot)
	}
}

func TestACorruptLedgerRefusesPaidCallsWhenALimitIsSet(t *testing.T) {
	f := setup(t)
	os.WriteFile(filepath.Join(f.dir, "spend_ledger.json"), []byte("{nope"), 0o644)
	// No daily/total limit: the ledger is not consulted to gate, so the call goes ahead.
	if resp := gen(t, f, Request{URN: "a", Mode: ModeImage}); resp.UsedMode != ModeImage {
		t.Fatalf("with no limits a corrupt ledger must not block: %+v", resp)
	}
	f.s.Policy.Spend.DailyUSD = 5
	_, err := f.s.Generate(context.Background(), Request{URN: "b", Mode: ModeImage})
	if !errors.Is(err, spend.ErrCorrupt) {
		t.Fatalf("a limit that cannot be enforced must stop the paid call: %v", err)
	}
	if f.mod.calls.Load() != 1 {
		t.Fatal("the model was called although the limit could not be checked")
	}
}

func TestAPolicyChainIsFollowedInOrder(t *testing.T) {
	f := setup(t)
	f.s.Policy.Fallback[ModeHybrid] = []Mode{ModeImage, ModeSVG}
	f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"}
	resp := gen(t, f, Request{URN: "a"}) // hybrid -> image -> svg, and the model is down for both
	if resp.UsedMode != ModeSVG || *resp.FallbackReason != "credential_missing" {
		t.Fatalf("%+v", resp)
	}
	// Only hybrid is broken: the chain lands on image, not svg.
	f2 := setup(t)
	f2.s.Policy.Fallback[ModeHybrid] = []Mode{ModeImage, ModeSVG}
	f2.s.Engines[ModeHybrid] = failing{ModeHybrid}
	resp = gen(t, f2, Request{URN: "a"})
	if resp.UsedMode != ModeImage || resp.FallbackReason == nil || *resp.FallbackReason != "model_error" {
		t.Fatalf("hybrid should fall to image: %+v", resp)
	}
}

// failing is an engine that always declines.
type failing struct{ mode engine.Mode }

func (f failing) Mode() engine.Mode { return f.mode }
func (failing) Capabilities() engine.Capabilities {
	return engine.Capabilities{Paid: true, EstimateUSD: 0.034}
}
func (failing) Preflight() *engine.Unavailable {
	return &engine.Unavailable{Reason: engine.ModelError, Detail: "down"}
}
func (failing) Generate(context.Context, engine.Call) (*engine.Output, error) {
	return nil, errors.New("never reached")
}

func TestAChainWithNothingFreeEndsInAnError(t *testing.T) {
	f := setup(t)
	f.s.Policy.Fallback[ModeHybrid] = []Mode{ModeImage}
	f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"}
	_, err := f.s.Generate(context.Background(), Request{URN: "a"})
	if code(err) != "credential_missing" {
		t.Fatalf("got %v", err)
	}
	if f.s.Store.Exists("a") {
		t.Fatal("nothing may be written when no engine in the chain ran")
	}
}

func TestTheSpriteIsDrawnOncePerRequestEvenWhenTheFallbackNeedsItToo(t *testing.T) {
	f := setup(t)
	f.mod.genErr = &model.Error{Reason: model.ModelError, Detail: "boom"}
	gen(t, f, Request{URN: "a"}) // hybrid draws the sprite, the model fails, svg falls back
	if f.svg.calls.Load() != 1 {
		t.Fatalf("the sprite engine ran %d times; the fallback must reuse the sprite drawn for hybrid", f.svg.calls.Load())
	}
}

func TestStatusReportsAvailabilityAndSpend(t *testing.T) {
	f := setup(t)
	f.s.Policy.Spend.DailyUSD = 1
	gen(t, f, Request{URN: "a", Mode: ModeImage})
	st := f.s.Status(context.Background())
	byMode := map[Mode]ModeStatus{}
	for _, m := range st.Modes {
		byMode[m.Mode] = m
	}
	if !byMode[ModeSVG].Available || !byMode[ModeSVG].Capabilities.Reproducible || byMode[ModeSVG].Capabilities.Paid {
		t.Errorf("svg: %+v", byMode[ModeSVG])
	}
	if !byMode[ModeHybrid].Available || !byMode[ModeHybrid].Capabilities.Paid || !byMode[ModeHybrid].Capabilities.NeedsIdentity {
		t.Errorf("hybrid: %+v", byMode[ModeHybrid])
	}
	if got := byMode[ModeHybrid].Fallback; len(got) != 1 || got[0] != ModeSVG {
		t.Errorf("hybrid fallback = %v", got)
	}
	if st.Spend.SpentTotal != 0.034 || st.Spend.Calls != 1 || st.Spend.Limits.DailyUSD != 1 {
		t.Errorf("spend: %+v", st.Spend)
	}
	f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"}
	if m := f.s.Status(context.Background()).Modes[1]; m.Available || m.Reason != engine.CredentialMissing {
		t.Errorf("image with no credential: %+v", m)
	}
	g := setup(t)
	g.s.Engines = Engines(g.svg, nil, 0.034, g.dir)
	for _, m := range g.s.Status(context.Background()).Modes {
		if m.Mode != ModeSVG && (m.Available || m.Reason != engine.Unconfigured) {
			t.Errorf("an unconfigured mode must say so: %+v", m)
		}
	}
}

func TestAFallbackStandInIsUpgradedWhenTheModelComesBack(t *testing.T) {
	f := setup(t)
	f.mod.pre = &model.Error{Reason: model.CredentialMissing, Detail: "no key"}
	first := gen(t, f, Request{URN: "agt-1"})
	if first.UsedMode != ModeSVG {
		t.Fatal("setup: want the stand-in")
	}
	// Still unavailable: the stand-in is returned, nothing churns.
	svgCalls := f.svg.calls.Load()
	same := gen(t, f, Request{URN: "agt-1"})
	if !same.Existed || f.svg.calls.Load() != svgCalls {
		t.Fatalf("with the model still down, the stand-in must be returned as is: %+v", same)
	}
	// The model is back: hybrid regenerates without force and keeps the stand-in.
	f.mod.pre = nil
	up := gen(t, f, Request{URN: "agt-1"})
	if up.Existed || up.UsedMode != ModeHybrid || up.FallbackReason != nil {
		t.Fatalf("not upgraded: %+v", up)
	}
	archived, _ := filepath.Glob(filepath.Join(f.dir, "agt-1.2026*Z.png"))
	if len(archived) != 1 {
		t.Fatalf("the stand-in must be kept, archived files: %v", archived)
	}
	// A real model avatar is never upgraded, and svg-by-choice is not a stand-in.
	if again := gen(t, f, Request{URN: "agt-1"}); !again.Existed || again.UsedMode != ModeHybrid {
		t.Fatalf("%+v", again)
	}
	g := setup(t)
	gen(t, g, Request{URN: "agt-2", Mode: ModeSVG})
	if r := gen(t, g, Request{URN: "agt-2", Mode: ModeHybrid}); !r.Existed || r.UsedMode != ModeSVG {
		t.Fatalf("an svg chosen on purpose is not a stand-in: %+v", r)
	}
}

func TestForceKeepsTheOldFace(t *testing.T) {
	f := setup(t)
	first := gen(t, f, Request{URN: "agt-1", Mode: ModeSVG})
	old, _ := os.ReadFile(first.Files[0].Path)
	f.s.Now = func() time.Time { return time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC) }
	second := gen(t, f, Request{URN: "agt-1", Mode: ModeSVG, Force: true})
	if second.Existed {
		t.Fatal("force regenerates")
	}
	archived, _ := filepath.Glob(filepath.Join(f.dir, "agt-1.20261015T020000.000Z.png"))
	if len(archived) != 1 {
		t.Fatalf("archived: %v", archived)
	}
	if b, _ := os.ReadFile(archived[0]); !bytes.Equal(b, old) {
		t.Fatal("the archived file is not the old face")
	}
}

func TestAFailedForceNeverLeavesAgentsWithoutAFace(t *testing.T) {
	f := setup(t)
	gen(t, f, Request{URN: "agt-1", Mode: ModeImage})
	want, _ := os.ReadFile(f.s.Store.Paths("agt-1").PNG)
	f.mod.genErr = &model.Error{Reason: model.ModelError, Detail: "boom"}
	if _, err := f.s.Generate(context.Background(), Request{URN: "agt-1", Mode: ModeImage, Force: true, Fallback: "none"}); err == nil {
		t.Fatal("setup: want a failure")
	}
	got, err := os.ReadFile(f.s.Store.Paths("agt-1").PNG)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("the existing avatar was lost by a failed force: %v", err)
	}
	f.svg.err = errors.New("hadron down")
	f.mod.genErr = nil
	if _, err := f.s.Generate(context.Background(), Request{URN: "agt-1", Mode: ModeHybrid, Force: true}); err == nil {
		t.Fatal("setup: want a failure")
	}
	if got, _ := os.ReadFile(f.s.Store.Paths("agt-1").PNG); !bytes.Equal(got, want) {
		t.Fatal("the existing avatar was lost when the sprite engine failed")
	}
}

func TestCollidingNamesAreRefused(t *testing.T) {
	f := setup(t)
	gen(t, f, Request{URN: "a/b:c", Mode: ModeSVG})
	_, err := f.s.Generate(context.Background(), Request{URN: "a_b_c", Mode: ModeSVG})
	if !errors.Is(err, store.ErrCollision) {
		t.Fatalf("got %v", err)
	}
	if again := gen(t, f, Request{URN: "a/b:c", Mode: ModeSVG}); !again.Existed {
		t.Fatal("the owner must still get its own file")
	}
}

func TestUnicodeNamesAreStoredNFC(t *testing.T) {
	f := setup(t)
	resp := gen(t, f, Request{URN: "Ünï-cöde", Mode: ModeSVG})
	if filepath.Base(resp.Files[0].Path) != "Ünï-cöde.png" {
		t.Fatalf("got %q", filepath.Base(resp.Files[0].Path))
	}
	nfd := norm.NFD.String("Ünï-cöde") // the decomposed spelling of the same name
	if nfd == "Ünï-cöde" {
		t.Fatal("test premise: NFD must differ in bytes")
	}
	if again := gen(t, f, Request{URN: nfd, Mode: ModeSVG}); !again.Existed {
		t.Fatal("NFD and NFC spellings are the same agent")
	}
}

func TestExplicitAndNamedPalettes(t *testing.T) {
	f := setup(t)
	r := gen(t, f, Request{URN: "a", Mode: ModeSVG, Palette: "violet"})
	if r.Provenance.Palette.Slot != "violet" || f.svg.last.Accent != "#9B6BFF" {
		t.Fatalf("named slot: %+v %q", r.Provenance.Palette, f.svg.last.Accent)
	}
	r = gen(t, f, Request{URN: "b", Mode: ModeSVG, Accent: "#112233", BG: "#445566"})
	if r.Provenance.Palette.Slot != "custom" || f.svg.last.Accent != "#112233" || r.Provenance.Palette.BG != "#445566" {
		t.Fatalf("explicit pair: %+v", r.Provenance.Palette)
	}
	// Neither touched the registry: only registry-assigned agents live there.
	if _, ok, _ := f.s.Registry.Peek("a"); ok {
		t.Fatal("an explicit palette must not consume a registry slot")
	}
	gen(t, f, Request{URN: "c", Mode: ModeSVG})
	if _, ok, _ := f.s.Registry.Peek("c"); !ok {
		t.Fatal("a default palette is the registry's")
	}
}

func TestRequestsAreValidatedBeforeAnythingRuns(t *testing.T) {
	seven := 101
	cases := map[string]Request{
		"urn":        {URN: "  "},
		"mode":       {URN: "a", Mode: "photo"},
		"fallback":   {URN: "a", Fallback: "maybe"},
		"role":       {URN: "a", Role: "line\nbreak"},
		"mood":       {URN: "a", Mood: strings.Repeat("x", MaxLabel+1)},
		"fill":       {URN: "a", Fill: &seven},
		"budget_usd": {URN: "a", BudgetUSD: -1},
		"accent":     {URN: "a", Accent: "#112233"},
		"palette":    {URN: "a", Palette: "violet", Accent: "#112233", BG: "#445566"},
		"bg":         {URN: "a", Accent: "#112233", BG: "blue"},
	}
	unknown := Request{URN: "a", Palette: "chartreuse"}
	cases["palette "] = unknown
	for field, r := range cases {
		f := setup(t)
		_, err := f.s.Generate(context.Background(), r)
		var fe *avatar.FieldError
		if !errors.As(err, &fe) || fe.Field != strings.TrimSpace(field) {
			t.Errorf("%s: got %v", field, err)
		}
		if f.svg.calls.Load()+f.mod.calls.Load() != 0 {
			t.Errorf("%s: an engine ran for an invalid request", field)
		}
	}
}

func TestConcurrentRequestsForOneAgentGenerateOnce(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.s.Generate(context.Background(), Request{URN: "agt-1"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.mod.calls.Load() != 1 {
		t.Fatalf("8 concurrent requests must cost one model call, got %d", f.mod.calls.Load())
	}
}

func TestASpriteEngineFailureIsReportedNotHidden(t *testing.T) {
	f := setup(t)
	f.svg.err = errors.New("hadron down")
	for _, mode := range []Mode{ModeSVG, ModeHybrid} {
		if _, err := f.s.Generate(context.Background(), Request{URN: "agt-" + string(mode), Mode: mode}); err == nil || !strings.Contains(err.Error(), "hadron down") {
			t.Errorf("%s: got %v", mode, err)
		}
	}
}
