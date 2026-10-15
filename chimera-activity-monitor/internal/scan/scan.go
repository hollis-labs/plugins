// Package scan discovers live coding agents on this machine without being
// their launcher. Linux only for the spike. It never reads process
// environments or credential files: only the named fields below.
package scan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Confidence mirrors go-runtime-events' tiers.
type Confidence string

const (
	Exact    Confidence = "exact"
	Derived  Confidence = "derived"
	Inferred Confidence = "inferred"
)

// Agent is one live agent process. Conf records the tier per field.
type Agent struct {
	ID           string                `json:"id"` // provider:pid:starttime
	Provider     string                `json:"provider"`
	PID          int                   `json:"pid"`
	SessionID    string                `json:"session_id,omitempty"`
	Cwd          string                `json:"cwd,omitempty"`
	State        string                `json:"state"` // idle|busy|waiting|unknown
	WaitingFor   string                `json:"waiting_for,omitempty"`
	StartedAt    *time.Time            `json:"started_at,omitempty"`
	LastActivity *time.Time            `json:"last_activity,omitempty"`
	Conf         map[string]Confidence `json:"confidence"`
}

// Scanner holds the roots so tests can point at fixtures.
type Scanner struct {
	Proc      string // default /proc
	ClaudeDir string // default ~/.claude/sessions
}

func New() *Scanner {
	home, _ := os.UserHomeDir()
	return &Scanner{Proc: "/proc", ClaudeDir: filepath.Join(home, ".claude", "sessions")}
}

// Scan returns agents sorted by provider then pid.
func (s *Scanner) Scan(ctx context.Context) ([]Agent, error) {
	var out []Agent
	claude := map[int]bool{}
	for _, a := range s.claude() {
		claude[a.PID] = true
		out = append(out, a)
	}
	for _, a := range s.procAgents(claude) {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].PID < out[j].PID
	})
	return out, ctx.Err()
}

// startTicks reads field 22 of /proc/<pid>/stat (process start, clock ticks).
func (s *Scanner) startTicks(pid int) (string, bool) {
	b, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	// comm may contain spaces/parens: split after the last ')'.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 20 { // field 22 is index 19 after the comm field
		return "", false
	}
	return f[19], true
}

func (s *Scanner) comm(pid int) string {
	b, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// claudeFile decodes only the fields we use; nothing else is retained.
type claudeFile struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"`
	ProcStart       string `json:"procStart"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

func (s *Scanner) claude() []Agent {
	files, _ := filepath.Glob(filepath.Join(s.ClaudeDir, "*.json"))
	var out []Agent
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var c claudeFile
		if json.Unmarshal(b, &c) != nil || c.PID == 0 {
			continue
		}
		// Files are never deleted on exit: verify pid and start time.
		if st, ok := s.startTicks(c.PID); !ok || st != c.ProcStart || s.comm(c.PID) != "claude" {
			continue
		}
		a := Agent{
			ID: "claude:" + strconv.Itoa(c.PID) + ":" + c.ProcStart, Provider: "claude", PID: c.PID,
			SessionID: c.SessionID, Cwd: c.Cwd, State: normState(c.Status), WaitingFor: c.WaitingFor,
			Conf: map[string]Confidence{"alive": Exact, "state": Exact, "session_id": Exact, "cwd": Exact},
		}
		if c.StartedAt > 0 {
			t := time.UnixMilli(c.StartedAt).UTC()
			a.StartedAt = &t
			a.Conf["started_at"] = Exact
		}
		if c.StatusUpdatedAt > 0 {
			t := time.UnixMilli(c.StatusUpdatedAt).UTC()
			a.LastActivity = &t
			a.Conf["last_activity"] = Derived // status change time, not last output
		}
		out = append(out, a)
	}
	return out
}

func normState(s string) string {
	switch s {
	case "idle", "busy", "waiting":
		return s
	}
	return "unknown"
}

// procAgents finds codex and agy by process name. State is not observable
// from the process table alone, so it is reported as unknown/inferred.
func (s *Scanner) procAgents(skip map[int]bool) []Agent {
	ents, _ := os.ReadDir(s.Proc)
	var out []Agent
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || skip[pid] {
			continue
		}
		name := s.comm(pid)
		if name != "codex" && name != "agy" {
			continue
		}
		if name == "codex" && s.isCodexHelper(pid) {
			continue
		}
		st, ok := s.startTicks(pid)
		if !ok {
			continue
		}
		a := Agent{
			ID: name + ":" + strconv.Itoa(pid) + ":" + st, Provider: name, PID: pid, State: "unknown",
			Conf: map[string]Confidence{"alive": Exact, "state": Inferred},
		}
		if cwd, err := os.Readlink(filepath.Join(s.Proc, strconv.Itoa(pid), "cwd")); err == nil {
			a.Cwd = cwd
			a.Conf["cwd"] = Derived // launcher dir, not necessarily the project
		}
		out = append(out, a)
	}
	return out
}

// isCodexHelper reports whether this codex process is the shared app-server
// daemon or its proxy rather than an interactive/exec session. It only
// compares argv tokens against fixed subcommand names; argv is never stored
// or returned because it can carry hook commands and prompts.
func (s *Scanner) isCodexHelper(pid int) bool {
	b, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	for _, tok := range strings.Split(string(b), "\x00") {
		if tok == "app-server" || tok == "proxy" {
			return true
		}
	}
	return false
}
