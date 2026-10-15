package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeProc writes a /proc/<pid> entry. stat field 22 (start ticks) is the 20th
// field after the closing paren of comm.
func fakeProc(t *testing.T, root string, pid, ticks, comm string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := pid + " (" + comm + ") S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 " + ticks + " 0 0\n"
	os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644)
	os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644)
	os.Symlink("/work/"+comm, filepath.Join(dir, "cwd"))
}

func session(t *testing.T, dir, pid, procStart, status string) {
	t.Helper()
	body := `{"pid":` + pid + `,"sessionId":"s-` + pid + `","cwd":"/p","startedAt":1000,"procStart":"` + procStart +
		`","status":"` + status + `","statusUpdatedAt":2000,"messagingSocketPath":"/secret.sock"}`
	os.WriteFile(filepath.Join(dir, pid+".json"), []byte(body), 0o644)
}

func TestScanVerifiesClaudeAndFindsCodex(t *testing.T) {
	proc, sess := t.TempDir(), t.TempDir()
	fakeProc(t, proc, "10", "500", "claude")
	fakeProc(t, proc, "11", "600", "claude") // session file says a different start: pid reuse
	fakeProc(t, proc, "12", "700", "codex")
	fakeProc(t, proc, "13", "800", "bash")
	fakeProc(t, proc, "14", "900", "codex") // app-server daemon: infrastructure, not an agent
	os.WriteFile(filepath.Join(proc, "14", "cmdline"), []byte("/x/codex\x00app-server\x00--listen\x00"), 0o644)
	os.WriteFile(filepath.Join(proc, "12", "cmdline"), []byte("codex\x00-c\x00hooks.SessionStart=[]\x00"), 0o644)
	session(t, sess, "10", "500", "waiting")
	session(t, sess, "11", "111", "idle")
	session(t, sess, "99", "1", "idle") // process gone
	os.WriteFile(filepath.Join(sess, "10.abc.key"), []byte("not json, never read"), 0o600)

	got, err := (&Scanner{Proc: proc, ClaudeDir: sess}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want claude:10 and codex:12 only (not the app-server daemon), got %+v", got)
	}
	c, x := got[0], got[1]
	if c.Provider != "claude" || c.PID != 10 || c.State != "waiting" || c.Conf["state"] != Exact {
		t.Errorf("claude agent wrong: %+v", c)
	}
	if x.Provider != "codex" || x.PID != 12 || x.State != "unknown" || x.Conf["state"] != Inferred {
		t.Errorf("codex agent wrong: %+v", x)
	}
}

func TestTrackerEmitsStartChangeExit(t *testing.T) {
	tr := NewTracker(100)
	now := time.Unix(1000, 0)
	a := Agent{ID: "claude:1:5", Provider: "claude", PID: 1, State: "idle"}
	if evs := tr.Update(now, []Agent{a}); len(evs) != 2 || evs[0].Kind != "process.started" || evs[1].Kind != "session.idle" {
		t.Fatalf("first sighting: %+v", evs)
	}
	if evs := tr.Update(now, []Agent{a}); len(evs) != 0 {
		t.Fatalf("no change must emit nothing, got %d", len(evs))
	}
	a.State = "busy"
	if evs := tr.Update(now, []Agent{a}); len(evs) != 1 || evs[0].Kind != "session.processing" {
		t.Fatalf("state change: %+v", evs)
	}
	if evs := tr.Update(now, nil); len(evs) != 1 || evs[0].Kind != "process.exited" {
		t.Fatalf("exit: %+v", evs)
	}
	if evs, latest := tr.Since(2); latest != 4 || len(evs) != 2 {
		t.Fatalf("since: latest=%d n=%d", latest, len(evs))
	}
}
