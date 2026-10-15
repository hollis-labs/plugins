package scan

import (
	"encoding/json"
	"sync"
	"time"

	re "github.com/hollis-labs/go-runtime-events/runtimeevents"
)

// Channels for unmanaged discovery. go-runtime-events' SourceChannel set has
// no value for these yet (open string type; gap noted for CW-20261014-0144).
const (
	ChannelProcScan    re.SourceChannel = "proc-scan"
	ChannelSessionFile re.SourceChannel = "session-file"
)

// Tracker diffs successive scans into runtime-event envelopes and keeps a
// bounded ring so clients can poll with ?since=<sequence>.
type Tracker struct {
	mu   sync.Mutex
	prev map[string]Agent
	seq  uint64
	ring []re.Event
	max  int
}

func NewTracker(max int) *Tracker { return &Tracker{prev: map[string]Agent{}, max: max} }

func (t *Tracker) Update(now time.Time, agents []Agent) []re.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur := map[string]Agent{}
	var evs []re.Event
	for _, a := range agents {
		cur[a.ID] = a
		old, seen := t.prev[a.ID]
		switch {
		case !seen:
			evs = append(evs, t.mk(now, a, re.KindProcessStarted))
			evs = append(evs, t.mk(now, a, stateKind(a.State)))
		case old.State != a.State || old.WaitingFor != a.WaitingFor:
			evs = append(evs, t.mk(now, a, stateKind(a.State)))
		}
	}
	for id, old := range t.prev {
		if _, ok := cur[id]; !ok {
			evs = append(evs, t.mk(now, old, re.KindProcessExited))
		}
	}
	t.prev = cur
	t.ring = append(t.ring, evs...)
	if over := len(t.ring) - t.max; over > 0 {
		t.ring = append([]re.Event(nil), t.ring[over:]...)
	}
	return evs
}

func stateKind(s string) re.EventKind {
	switch s {
	case "busy":
		return re.KindSessionProcessing
	case "idle":
		return re.KindSessionIdle
	}
	return re.KindSessionHeartbeat // unknown/waiting: no dedicated kind yet
}

func (t *Tracker) mk(now time.Time, a Agent, k re.EventKind) re.Event {
	t.seq++
	ch, conf := ChannelProcScan, Derived
	if a.Provider == "claude" {
		ch, conf = ChannelSessionFile, Exact
	}
	payload, _ := json.Marshal(map[string]any{"state": a.State, "waiting_for": a.WaitingFor, "cwd": a.Cwd})
	return re.Event{
		SchemaVersion: re.SchemaVersion, ID: "evt_" + a.ID + "_" + itoa(t.seq), Kind: k, Time: now.UTC(),
		App: "activity-monitor", SessionID: a.ID, Sequence: t.seq,
		Process: re.Process{PID: a.PID, Provider: a.Provider, ProviderSessionID: a.SessionID},
		Source:  re.Source{Channel: ch, Confidence: re.Confidence(conf)},
		Payload: payload,
	}
}

func itoa(u uint64) string { return uitoa(u) }
func uitoa(u uint64) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

// Since returns events with sequence > after, plus the latest sequence.
func (t *Tracker) Since(after uint64) ([]re.Event, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []re.Event
	for _, e := range t.ring {
		if e.Sequence > after {
			out = append(out, e)
		}
	}
	return out, t.seq
}

func (t *Tracker) Agents() []Agent {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Agent, 0, len(t.prev))
	for _, a := range t.prev {
		out = append(out, a)
	}
	return out
}
