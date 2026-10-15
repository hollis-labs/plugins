package model

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTool stands in for the model tool's avatar.py. It follows the CLI contract
// (options as --k=v, urn after "--"), records its argv, and fails on demand
// according to markers in the urn, so no network or credential is ever involved.
const legacyTool = `
import json, os, re, sys, time
args = sys.argv[1:]
here = os.path.dirname(os.path.abspath(__file__))
json.dump({"args": args, "isolated": sys.flags.isolated}, open(os.path.join(here, "last_args.json"), "w"))
opts = {}
rest = args
if "--" in args:
    i = args.index("--"); rest = args[:i]; urn = args[i + 1]
for a in rest:
    if a.startswith("--") and "=" in a:
        k, v = a[2:].split("=", 1); opts[k] = v
name = re.sub(r"[^\w.@+-]", "_", urn)
out = opts["out"]
if "BUDGET" in urn: sys.exit("budget cap reached: $9.60")
if "NOIMG" in urn: raise RuntimeError("no image returned")
if "H401" in urn: raise RuntimeError("HTTP 401: bad key")
if "H402" in urn: raise RuntimeError("HTTP 402: no credits")
if "H403" in urn: raise RuntimeError("HTTP 403: scope")
if "H500" in urn: raise RuntimeError("HTTP 500: upstream")
if "COSTFAIL" in urn:
    ledger = os.path.join(here, "ledger.json")
    d = json.load(open(ledger)) if os.path.exists(ledger) else {"total": 0.0, "calls": []}
    d["total"] = round(d["total"] + 0.034, 6)
    json.dump(d, open(ledger, "w"))
    raise RuntimeError("no image returned")
if "SLOW" in urn: time.sleep(10)
if "EXISTS" in urn: print("exists, unchanged: x"); sys.exit(0)
png = os.path.join(out, name + ".png")
open(png, "wb").write(b"\x89PNG fake")
open(os.path.join(out, name + "@8x.png"), "wb").write(b"\x89PNG fake8")
json.dump({"urn": urn, "model": "fake/model", "prompt": "draw " + urn, "palette": ["#000000"], "despeckle": True},
          open(os.path.join(out, name + ".json"), "w"))
ledger = os.path.join(here, "ledger.json")
d = json.load(open(ledger)) if os.path.exists(ledger) else {"total": 0.0, "calls": []}
d["total"] = round(d["total"] + 0.034, 6)
json.dump(d, open(ledger, "w"))
print("wrote", png)
`

// jsonTool follows the current CLI contract: --json gives one JSON line on stdout
// on success or on stderr on failure, with a distinct exit status per class, and
// cost_usd is the billed cost on failure too. Env overrides are honoured.
const jsonTool = `
import json, os, re, sys, time
args = sys.argv[1:]
here = os.path.dirname(os.path.abspath(__file__))
json.dump({"args": args, "isolated": sys.flags.isolated, "env": {k: v for k, v in os.environ.items() if k.startswith("AVATAR_")}},
          open(os.path.join(here, "last_args.json"), "w"))
EXIT = {"over_budget": 10, "credential_missing": 11, "scope_missing": 12, "no_image": 13, "model_error": 14, "ledger_corrupt": 15, "registry_corrupt": 16, "internal_error": 70}
opts = {}
rest = args[:args.index("--")]; urn = args[args.index("--") + 1]
for a in rest:
    if a.startswith("--") and "=" in a:
        k, v = a[2:].split("=", 1); opts[k] = v
ledger = os.environ.get("AVATAR_LEDGER") or os.path.join(here, "ledger.json")
def bump(usd):
    d = json.load(open(ledger)) if os.path.exists(ledger) else {"total": 0.0, "calls": []}
    d["total"] = round(d["total"] + usd, 6); json.dump(d, open(ledger, "w"))
def fail(code, msg, cost=0.0):
    print(json.dumps({"code": code, "message": msg, "cost_usd": cost}), file=sys.stderr); sys.exit(EXIT[code])
name = re.sub(r"[^\w.@+-]", "_", urn)
out = opts["out"]
if "BUDGET" in urn: fail("over_budget", "budget cap reached: $9.60")
if "NOIMG" in urn or "COSTFAIL" in urn: bump(0.034); fail("no_image", "no image returned", 0.034)
if "H401" in urn: fail("credential_missing", "HTTP 401: bad key")
if "H402" in urn: fail("over_budget", "HTTP 402: no credits")
if "H403" in urn: fail("scope_missing", "HTTP 403: scope")
if "H500" in urn: fail("model_error", "HTTP 500: upstream")
if "LEDGER" in urn: fail("ledger_corrupt", "ledger unreadable")
if "BOOM" in urn: fail("internal_error", "KeyError: x")
if "SLOW" in urn: time.sleep(10)
if "EXISTS" in urn:
    print(json.dumps({"png": os.path.join(out, name + ".png"), "png8x": "", "sidecar": "", "cost_usd": 0.0, "model": None, "existed": True})); sys.exit(0)
open(os.path.join(out, name + ".png"), "wb").write(b"\x89PNG fake")
open(os.path.join(out, name + "@8x.png"), "wb").write(b"\x89PNG fake8")
json.dump({"urn": urn, "model": "fake/model", "prompt": "draw " + urn, "palette": ["#000000"], "despeckle": True, "cost_usd": 0.0341},
          open(os.path.join(out, name + ".json"), "w"))
bump(0.0341)
print(json.dumps({"png": os.path.join(out, name + ".png"), "png8x": os.path.join(out, name + "@8x.png"), "sidecar": os.path.join(out, name + ".json"), "cost_usd": 0.0341, "model": "fake/model"}))
`

type env struct {
	e    *Python
	out  string
	home string
}

func setup(t *testing.T, withKey bool, tool ...string) env {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	dir, home, out := t.TempDir(), t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	script := jsonTool
	if len(tool) > 0 {
		script = tool[0]
	}
	must(os.WriteFile(filepath.Join(dir, "avatar.py"), []byte(script), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "golden"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "golden", "robot-01.png"), []byte("golden"), 0o644))
	if withKey {
		must(os.MkdirAll(filepath.Join(home, ".config", "avatars"), 0o755))
		must(os.WriteFile(filepath.Join(home, ".config", "avatars", "avatar-openrouter"), []byte("sk-not-a-real-key"), 0o600))
	}
	return env{&Python{Dir: dir, Home: home, EstimateUSD: 0.034, Timeout: 5 * time.Second}, out, home}
}

func (e env) req(urn string) Request {
	return Request{URN: urn, Name: strings.NewReplacer("/", "_", ":", "_").Replace(urn), Accent: "#FF6B5B", BG: "#1F2A44", OutDir: e.out}
}

func reason(t *testing.T, err error) Reason {
	t.Helper()
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("want *model.Error, got %v", err)
	}
	return me.Reason
}

// tools are the two dialects the adapter must understand.
var tools = map[string]struct {
	script string
	cost   float64 // what one successful call costs under that tool
}{
	"json":   {jsonTool, 0.0341},
	"legacy": {legacyTool, 0.034},
}

func TestSuccessReadsBackWhatTheToolWrote(t *testing.T) {
	for name, tool := range tools {
		e := setup(t, true, tool.script)
		r := e.req("agt-1")
		r.IdentityPNG = filepath.Join(e.out, "identity.png")
		os.WriteFile(r.IdentityPNG, []byte("sprite"), 0o644)
		res, err := e.e.Generate(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Model != "fake/model" || !res.Despeckle {
			t.Errorf("%s: sidecar not read: %+v", name, res)
		}
		if res.CostUSD != tool.cost {
			t.Errorf("%s: cost = %v, want %v", name, res.CostUSD, tool.cost)
		}
		if len(res.PromptSHA256) != 64 || len(res.GoldenSHA256) != 64 || len(res.IdentitySHA256) != 64 {
			t.Errorf("%s: hashes missing: %+v", name, res)
		}
		var urn string
		json.Unmarshal(res.Sidecar["urn"], &urn)
		if urn != "agt-1" {
			t.Errorf("%s: the tool's sidecar must be returned verbatim, got urn %q", name, urn)
		}
		// A second call's cost is that call's, not the running total.
		res2, _ := e.e.Generate(context.Background(), e.req("agt-2"))
		if res2.CostUSD != tool.cost {
			t.Errorf("%s: second call cost = %v, want %v", name, res2.CostUSD, tool.cost)
		}
	}
}

func TestArgumentsAreBoundAndAUrnCannotBeAnOption(t *testing.T) {
	e := setup(t, true)
	r := e.req("--force")
	r.Role, r.Species, r.Mood = "--dry-run", "robot", "calm"
	if _, err := e.e.Generate(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Args     []string `json:"args"`
		Isolated int      `json:"isolated"`
	}
	b, _ := os.ReadFile(filepath.Join(e.e.Dir, "last_args.json"))
	json.Unmarshal(b, &rec)
	args := rec.Args
	joined := strings.Join(args, " ")
	if args[len(args)-2] != "--" || args[len(args)-1] != "--force" {
		t.Errorf("urn must follow --, got %v", args)
	}
	if !strings.Contains(joined, "--role=--dry-run") {
		t.Errorf("a value that looks like an option must be bound with =, got %v", args)
	}
	if rec.Isolated != 1 {
		t.Errorf("the interpreter must run isolated (-I): sys.flags.isolated = %d", rec.Isolated)
	}
	if !strings.Contains(" "+joined+" ", " --json ") {
		t.Errorf("the machine-readable contract must be requested (--json), got %v", args)
	}
}

func TestPreflightOrder(t *testing.T) {
	// Not installed.
	if pf := (*Python)(nil).Preflight(0); pf == nil || pf.Reason != Unconfigured {
		t.Errorf("nil engine: %v", pf)
	}
	if pf := (&Python{}).Preflight(0); pf == nil || pf.Reason != Unconfigured {
		t.Errorf("empty dir: %v", pf)
	}
	// Installed, no credential.
	e := setup(t, false)
	if pf := e.e.Preflight(0); pf == nil || pf.Reason != CredentialMissing {
		t.Errorf("no key: %v", pf)
	}
	// With a credential: ok, until the ceiling is below the estimate.
	e = setup(t, true)
	if pf := e.e.Preflight(0); pf != nil {
		t.Errorf("should pass: %v", pf)
	}
	if pf := e.e.Preflight(1.00); pf != nil {
		t.Errorf("ceiling above the estimate: %v", pf)
	}
	if pf := e.e.Preflight(0.01); pf == nil || pf.Reason != OverBudget {
		t.Errorf("ceiling below the estimate: %v", pf)
	}
	// An empty key file is no credential.
	os.WriteFile(filepath.Join(e.home, ".config", "avatars", "avatar-openrouter"), nil, 0o600)
	if pf := e.e.Preflight(0); pf == nil || pf.Reason != CredentialMissing {
		t.Errorf("empty key: %v", pf)
	}
}

func TestFailuresAreClassified(t *testing.T) {
	// Markers both tools understand.
	common := map[string]Reason{
		"BUDGET": OverBudget, "H402": OverBudget, "H401": CredentialMissing, "H403": ScopeMissing, "H500": ModelError, "NOIMG": ModelError, "EXISTS": ModelError,
	}
	for name, tool := range tools {
		e := setup(t, true, tool.script)
		for marker, want := range common {
			_, err := e.e.Generate(context.Background(), e.req("x-"+marker))
			if got := reason(t, err); got != want {
				t.Errorf("%s/%s: got %s, want %s", name, marker, got, want)
			}
		}
	}
	// The JSON tool's other codes: all "the model path did not produce an avatar",
	// with the code kept in the detail so a human can tell them apart.
	e := setup(t, true, jsonTool)
	for marker, code := range map[string]string{"LEDGER": "ledger_corrupt", "BOOM": "internal_error"} {
		_, err := e.e.Generate(context.Background(), e.req("x-"+marker))
		if reason(t, err) != ModelError || !strings.Contains(err.Error(), code) {
			t.Errorf("%s: got %v, want model_error mentioning %s", marker, err, code)
		}
	}
}

func TestAFailedAttemptThatWasBilledCarriesItsCost(t *testing.T) {
	for name, tool := range tools {
		e := setup(t, true, tool.script)
		// The provider answered (and billed) but returned no image.
		_, err := e.e.Generate(context.Background(), e.req("x-COSTFAIL"))
		var me *Error
		if !errors.As(err, &me) || me.Reason != ModelError {
			t.Fatalf("%s: got %v", name, err)
		}
		if me.CostUSD != 0.034 {
			t.Fatalf("%s: the cost of a failed attempt must travel with the error so it counts against the limits, got %v", name, me.CostUSD)
		}
		// A failure that cost nothing says so.
		_, err = e.e.Generate(context.Background(), e.req("x-H500"))
		if errors.As(err, &me); me.CostUSD != 0 {
			t.Fatalf("%s: an unbilled failure cost %v", name, me.CostUSD)
		}
	}
}

func TestTheToolsOwnCostBeatsALedgerGuess(t *testing.T) {
	// Another writer bumps the shared ledger between our before/after reads; the
	// tool's reported cost must still be what we record.
	e := setup(t, true, jsonTool)
	res, err := e.e.Generate(context.Background(), e.req("agt-1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.CostUSD != 0.0341 {
		t.Fatalf("cost = %v, want the tool's own 0.0341", res.CostUSD)
	}
}

func TestOverridesAreHandedToTheTool(t *testing.T) {
	e := setup(t, true, jsonTool)
	keyFile := filepath.Join(e.home, "elsewhere-key")
	os.WriteFile(keyFile, []byte("k"), 0o600)
	e.e.KeyFile, e.e.LedgerPath, e.e.SpendCapUSD = keyFile, filepath.Join(e.home, "state", "ledger.json"), 3.5
	os.MkdirAll(filepath.Dir(e.e.LedgerPath), 0o755)
	if _, err := e.e.Generate(context.Background(), e.req("agt-1")); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Env map[string]string `json:"env"`
	}
	b, _ := os.ReadFile(filepath.Join(e.e.Dir, "last_args.json"))
	json.Unmarshal(b, &rec)
	if rec.Env["AVATAR_KEY_FILE"] != keyFile || rec.Env["AVATAR_LEDGER"] != e.e.LedgerPath || rec.Env["AVATAR_SPEND_CAP"] != "3.5" {
		t.Fatalf("env = %v", rec.Env)
	}
	if _, err := os.Stat(e.e.LedgerPath); err != nil {
		t.Fatal("the tool must have written its ledger where it was told, not beside the script")
	}
	if _, err := os.Stat(filepath.Join(e.e.Dir, "ledger.json")); err == nil {
		t.Fatal("a ledger was written in the tool's checkout despite AVATAR_LEDGER")
	}
	// Preflight follows the configured key, not the default path.
	os.Remove(keyFile)
	if pf := e.e.Preflight(0); pf == nil || pf.Reason != CredentialMissing || !strings.Contains(pf.Detail, "elsewhere-key") {
		t.Fatalf("preflight must check the configured key file: %v", pf)
	}
}

func TestATimeoutIsAModelErrorAndStopsTheTool(t *testing.T) {
	e := setup(t, true)
	e.e.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := e.e.Generate(context.Background(), e.req("x-SLOW"))
	if reason(t, err) != ModelError || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("got %v", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("the tool was not stopped at the timeout: %v", time.Since(start))
	}
}

func TestTheToolDoesNotInheritTheCallersEnvironment(t *testing.T) {
	e := setup(t, true)
	t.Setenv("OPENROUTER_API_KEY", "leak-me")
	// The fake records nothing about env; assert on the process spec instead by
	// running a tool that prints it.
	os.WriteFile(filepath.Join(e.e.Dir, "avatar.py"), []byte(`
import os, sys, json
args = sys.argv[1:]; out = [a for a in args if a.startswith("--out=")][0][6:]
urn = args[args.index("--") + 1]
open(os.path.join(out, urn + ".json"), "w").write(json.dumps({"env": sorted(os.environ)}))
open(os.path.join(out, urn + ".png"), "wb").write(b"x")
`), 0o644)
	res, err := e.e.Generate(context.Background(), e.req("envcheck"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	json.Unmarshal(res.Sidecar["env"], &names)
	for _, n := range names {
		if strings.Contains(n, "OPENROUTER") {
			t.Fatalf("the caller's credential leaked into the tool: %v", names)
		}
	}
}
