// Package model is the model-backed avatar engine: an adapter around the
// model-side generator's CLI (avatar.py), which owns the model call, the palette
// snap, the downscale and the despeckle. Wrapping it rather than porting it
// keeps that pipeline free to change without a Go release; this package owns the
// seam: what is checked before a call, how a failure is classified, and what is
// read back.
//
// The CLI contract this relies on (the model tool's docs/CONTRACT.md):
//
//	python3 -I avatar.py --json [--role=R] [--species=S] [--mood=M] --accent=#hex --bg=#hex
//	        [--identity-ref=PNG --ref-style=sprite] --out=DIR -- <urn>
//
// It writes <name>.png (64x64), <name>@8x.png and <name>.json into DIR. With
// --json it prints one JSON line on success ({png, png8x, sidecar, cost_usd,
// model}) or, on failure, one JSON line on stderr ({code, message, cost_usd})
// and exits with a status per class. cost_usd is the billed cost, on failure too.
// A tool that predates --json is still understood: its text errors are matched
// and its cost is read from the ledger.
package model

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hollis-labs/station-plugins/avatar-server/internal/engine"
)

// Reason and Error are the engine package's: the model path is one engine, and
// its failures are the failures a fallback policy covers. The names stay here so
// this package reads on its own.
type (
	Reason = engine.Reason
	Error  = engine.Unavailable
)

const (
	CredentialMissing = engine.CredentialMissing
	ScopeMissing      = engine.ScopeMissing
	OverBudget        = engine.OverBudget
	ModelError        = engine.ModelError
	Unconfigured      = engine.Unconfigured
)

// Request is one model-backed generation.
type Request struct {
	URN, Name           string // Name is the file stem under OutDir
	Role, Species, Mood string
	Accent, BG          string
	IdentityPNG         string // the agent's sprite for hybrid; empty for image mode
	OutDir              string
}

// Result is what a successful call produced.
type Result struct {
	Model          string
	CostUSD        float64
	PromptSHA256   string
	GoldenSHA256   string
	IdentitySHA256 string
	Despeckle      bool
	// Sidecar is the model tool's own <name>.json, verbatim.
	Sidecar map[string]json.RawMessage
}

// Engine is the model path. *Python is the real one.
type Engine interface {
	// Preflight reports why a call cannot run, without making one. ceiling is the
	// per-call dollar limit (0 means none).
	Preflight(ceiling float64) *Error
	Generate(ctx context.Context, r Request) (*Result, error)
}

// Python runs the model tool's CLI.
type Python struct {
	// Dir is the model tool's checkout: it holds avatar.py, golden/ and ledger.json.
	Dir string
	// Python is the interpreter (default python3).
	Python string
	// EstimateUSD is the expected cost of one image, used for the budget gate.
	EstimateUSD float64
	// Timeout bounds one call (default 200s; the tool's own HTTP timeout is 180s).
	Timeout time.Duration
	// Home is where the tool looks for its credential (default $HOME).
	Home string
	// KeyFile, LedgerPath and SpendCapUSD are handed to the tool as AVATAR_KEY_FILE,
	// AVATAR_LEDGER and AVATAR_SPEND_CAP. Unset, the tool's own defaults apply (a
	// credential under Home, a ledger.json beside the script, a $9.50 cap).
	KeyFile     string
	LedgerPath  string
	SpendCapUSD float64

	mu sync.Mutex // one model call at a time: they share a spend ledger
}

const defaultTimeout = 200 * time.Second

func (p *Python) home() string {
	if p.Home != "" {
		return p.Home
	}
	h, _ := os.UserHomeDir()
	return h
}

func (p *Python) keyPath() string {
	if p.KeyFile != "" {
		return p.KeyFile
	}
	return filepath.Join(p.home(), ".config", "avatars", "avatar-openrouter")
}

func (p *Python) ledgerPath() string {
	if p.LedgerPath != "" {
		return p.LedgerPath
	}
	return filepath.Join(p.Dir, "ledger.json")
}

// Preflight checks, in order: the tool is installed, a credential exists, and the
// estimate fits the ceiling. It never reads the credential.
func (p *Python) Preflight(ceiling float64) *Error {
	if p == nil || p.Dir == "" {
		return &Error{Reason: Unconfigured, Detail: "no model tool is configured (AVATAR_MODEL_DIR)"}
	}
	for _, f := range []string{"avatar.py", filepath.Join("golden", "robot-01.png")} {
		if _, err := os.Stat(filepath.Join(p.Dir, f)); err != nil {
			return &Error{Reason: Unconfigured, Detail: fmt.Sprintf("model tool incomplete: %s: %v", f, err)}
		}
	}
	if st, err := os.Stat(p.keyPath()); err != nil || st.Size() == 0 {
		return &Error{Reason: CredentialMissing, Detail: "no model credential at " + p.keyPath()}
	}
	if ceiling > 0 && p.EstimateUSD > ceiling {
		return &Error{Reason: OverBudget, Detail: fmt.Sprintf("estimated $%.3f exceeds the $%.3f ceiling", p.EstimateUSD, ceiling)}
	}
	return nil
}

// Generate runs one call. Output files land in r.OutDir; the caller reads them.
func (p *Python) Generate(ctx context.Context, r Request) (*Result, error) {
	if pf := p.Preflight(0); pf != nil {
		return nil, pf
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	py := p.Python
	if py == "" {
		py = "python3"
	}
	args := []string{"-I", filepath.Join(p.Dir, "avatar.py"), "--json", "--out=" + r.OutDir, "--accent=" + r.Accent, "--bg=" + r.BG}
	for flag, v := range map[string]string{"--role=": r.Role, "--species=": r.Species, "--mood=": r.Mood} {
		if v != "" {
			args = append(args, flag+v)
		}
	}
	if r.IdentityPNG != "" {
		args = append(args, "--identity-ref="+r.IdentityPNG, "--ref-style=sprite")
	}
	// "--" so a urn that starts with a dash is a name, not an option.
	args = append(args, "--", r.URN)

	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.spent()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, args...)
	cmd.Dir = p.Dir
	// Only what the tool needs: no ambient credentials from this process's env.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + p.home(), "LANG=C.UTF-8", "PYTHONIOENCODING=utf-8"}
	if p.KeyFile != "" {
		cmd.Env = append(cmd.Env, "AVATAR_KEY_FILE="+p.KeyFile)
	}
	if p.LedgerPath != "" {
		cmd.Env = append(cmd.Env, "AVATAR_LEDGER="+p.LedgerPath)
	}
	if p.SpendCapUSD > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("AVATAR_SPEND_CAP=%g", p.SpendCapUSD))
	}
	var out, errb capped
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &Error{Reason: ModelError, Detail: fmt.Sprintf("the model tool did not finish within %s", timeout), CostUSD: round6(max(0, p.spent()-before))}
		}
		e := classifyFailure(errb.String(), out.String(), err)
		if !e.hasCost { // an older tool, or a crash before it could say: infer from the ledger
			e.CostUSD = round6(max(0, p.spent()-before))
		}
		return nil, &e.Error
	}
	line := parseResultLine(out.String())
	if line.Existed || strings.Contains(out.String(), "exists, unchanged") {
		return nil, &Error{Reason: ModelError, Detail: "the model tool found an existing file where none was expected"}
	}

	res := &Result{CostUSD: round6(max(0, p.spent()-before))}
	if line.ok && line.hasCost {
		res.CostUSD = round6(line.CostUSD) // the tool's own figure beats a before/after guess
	}
	sc, err := readJSON(filepath.Join(r.OutDir, r.Name+".json"))
	if err != nil {
		return nil, &Error{Reason: ModelError, Detail: "the model tool did not write a readable sidecar: " + err.Error()}
	}
	if _, err := os.Stat(filepath.Join(r.OutDir, r.Name+".png")); err != nil {
		return nil, &Error{Reason: ModelError, Detail: "the model tool did not write " + r.Name + ".png"}
	}
	res.Sidecar = sc
	var s string
	if json.Unmarshal(sc["model"], &s) == nil {
		res.Model = s
	}
	if json.Unmarshal(sc["prompt"], &s) == nil && s != "" {
		res.PromptSHA256 = sum([]byte(s))
	}
	_ = json.Unmarshal(sc["despeckle"], &res.Despeckle)
	res.GoldenSHA256 = fileSum(filepath.Join(p.Dir, "golden", "robot-01.png"))
	if r.IdentityPNG != "" {
		res.IdentitySHA256 = fileSum(r.IdentityPNG)
	}
	return res, nil
}

var httpRe = regexp.MustCompile(`HTTP (\d{3})`)

// failure is a classified failure plus whether the tool reported its own cost.
type failure struct {
	Error
	hasCost bool
}

// resultLine is the tool's one-line --json answer, success or failure.
type resultLine struct {
	ok      bool
	Code    string  `json:"code"`
	Message string  `json:"message"`
	CostUSD float64 `json:"cost_usd"`
	Existed bool    `json:"existed"`
	hasCost bool
}

// parseResultLine reads the last non-empty line of out as the tool's JSON.
func parseResultLine(out string) resultLine {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var rl resultLine
	if !strings.HasPrefix(last, "{") || json.Unmarshal([]byte(last), &rl) != nil {
		return resultLine{}
	}
	var probe map[string]json.RawMessage
	_ = json.Unmarshal([]byte(last), &probe)
	_, rl.hasCost = probe["cost_usd"]
	rl.ok = true
	return rl
}

// reasonOf maps the tool's error codes onto the reasons a fallback policy sees.
// no_image, the ledger and registry codes and internal_error are all "the model
// path did not produce an avatar", so they are model_error with the code kept in
// the detail.
func reasonOf(code string) Reason {
	switch code {
	case "over_budget":
		return OverBudget
	case "credential_missing":
		return CredentialMissing
	case "scope_missing":
		return ScopeMissing
	default:
		return ModelError
	}
}

// classifyFailure turns the tool's failure into a reason. It prefers the tool's
// JSON line (code and billed cost); for an older tool it reads the message text.
func classifyFailure(stderr, stdout string, runErr error) failure {
	if rl := parseResultLine(stderr); rl.ok && rl.Code != "" {
		detail := rl.Message
		if r := reasonOf(rl.Code); r == ModelError && rl.Code != "model_error" {
			detail = rl.Code + ": " + rl.Message
		}
		return failure{Error: Error{Reason: reasonOf(rl.Code), Detail: tail(detail, 300), CostUSD: round6(rl.CostUSD)}, hasCost: rl.hasCost}
	}
	return failure{Error: *classify(stderr, stdout, runErr)}
}

// classify reads a failure's text: the fallback for a tool with no --json.
func classify(stderr, stdout string, runErr error) *Error {
	text := stderr + "\n" + stdout
	detail := tail(strings.TrimSpace(stderr), 300)
	if detail == "" {
		detail = runErr.Error()
	}
	switch {
	case strings.Contains(text, "budget cap reached"):
		return &Error{Reason: OverBudget, Detail: detail}
	case strings.Contains(text, "avatar-openrouter") && strings.Contains(text, "No such file"):
		return &Error{Reason: CredentialMissing, Detail: detail}
	}
	if m := httpRe.FindStringSubmatch(text); m != nil {
		switch m[1] {
		case "401":
			return &Error{Reason: CredentialMissing, Detail: detail}
		case "403":
			return &Error{Reason: ScopeMissing, Detail: detail}
		case "402":
			return &Error{Reason: OverBudget, Detail: detail}
		}
	}
	return &Error{Reason: ModelError, Detail: detail}
}

func (p *Python) spent() float64 {
	var d struct {
		Total float64 `json:"total"`
	}
	b, err := os.ReadFile(p.ledgerPath())
	if err != nil || json.Unmarshal(b, &d) != nil {
		return 0
	}
	return d.Total
}

func readJSON(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	return m, json.Unmarshal(b, &m)
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func fileSum(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sum(b)
}

func round6(v float64) float64 { return float64(int64(v*1e6+0.5)) / 1e6 }

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// capped keeps at most 64 KiB of a process's output.
type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := 64<<10 - c.b.Len(); room > 0 {
		c.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
func (c *capped) String() string { return c.b.String() }
