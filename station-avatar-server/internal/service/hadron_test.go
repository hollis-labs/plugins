package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hollis-labs/station-plugins/avatar-server/internal/avatar"
	"github.com/hollis-labs/station-plugins/avatar-server/internal/hadron"
)

type fakeRunner struct {
	runID  string
	inputs map[string]any
	out    hadron.Outputs
	err    error
}

func (f *fakeRunner) Run(_ context.Context, runID string, in map[string]any) (hadron.Outputs, error) {
	f.runID, f.inputs = runID, in
	return f.out, f.err
}

func TestHadronSVGSendsTheWorkflowsInputs(t *testing.T) {
	f := &fakeRunner{out: hadron.Outputs{"svg": json.RawMessage(`"<svg/>"`), "sprite": json.RawMessage(`{"hue":5}`)}}
	r := avatar.Request{ID: "agt-1", Knobs: avatar.Knobs{Fill: 40, Hue: -1}, Accent: "#FF6B5B"}
	res, err := HadronSVG{f}.Generate(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res.SVG != "<svg/>" || res.RunID != r.RunID() || f.runID != r.RunID() {
		t.Fatalf("%+v / %s", res, f.runID)
	}
	want := map[string]any{"id": "agt-1", "fill": 40, "hue": -1, "accent": "#FF6B5B"}
	for k, v := range want {
		if f.inputs[k] != v {
			t.Errorf("input %s = %v, want %v", k, f.inputs[k], v)
		}
	}
}

func TestHadronSVGRefusesWhatTheWorkflowWouldRefuse(t *testing.T) {
	f := &fakeRunner{}
	if _, err := (HadronSVG{f}).Generate(context.Background(), avatar.Request{ID: "a", Knobs: avatar.Knobs{Fill: 500, Hue: -1}}); err == nil {
		t.Fatal("an out-of-range knob must be refused before Hadron is called")
	}
	if f.runID != "" {
		t.Fatal("Hadron was called for an invalid request")
	}
}

func TestHadronSVGReportsAnOutOfDateWorkflow(t *testing.T) {
	for _, out := range []hadron.Outputs{
		{"sprite": json.RawMessage(`{"hue":1}`)},
		{"svg": json.RawMessage(`""`), "sprite": json.RawMessage(`{"hue":1}`)},
		{"svg": json.RawMessage(`"<svg/>"`)},
	} {
		_, err := HadronSVG{&fakeRunner{out: out}}.Generate(context.Background(), avatar.Request{ID: "a", Knobs: avatar.Defaults()})
		if !errors.Is(err, ErrBadOutput) {
			t.Errorf("%v: got %v", out, err)
		}
	}
}

func TestHadronSVGPassesRunnerErrorsThrough(t *testing.T) {
	_, err := HadronSVG{&fakeRunner{err: hadron.ErrUnavailable}}.Generate(context.Background(), avatar.Request{ID: "a", Knobs: avatar.Defaults()})
	if !errors.Is(err, hadron.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}
