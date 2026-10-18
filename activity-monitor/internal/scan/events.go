package scan

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"

	re "github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
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
	mu         sync.Mutex
	prev       map[string]Agent
	seq        uint64
	ring       []re.Event
	max        int
	generation uint64
}

func NewTracker(max int) *Tracker {
	return &Tracker{prev: map[string]Agent{}, max: max, generation: uint64(time.Now().UnixNano())}
}

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
		case old.State != a.State || old.WaitingFor != a.WaitingFor || old.Stale != a.Stale || !bytes.Equal(old.LastAction, a.LastAction) || !sameTime(old.LastActivity, a.LastActivity):
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
	if a.Source == "pulse" {
		ch, conf = "plugin-hook", a.Conf["state"]
	}
	payload, _ := json.Marshal(a)
	return re.Event{
		SchemaVersion: re.SchemaVersion, ID: "evt_" + rand.Text(), Kind: k, Time: now.UTC(),
		App: "activity-monitor", SessionID: a.ID, Sequence: t.seq, Generation: t.generation,
		Process: re.Process{PID: a.PID, Provider: a.Provider, ProviderSessionID: a.SessionID},
		Source:  re.Source{Channel: ch, Confidence: re.Confidence(conf)},
		Payload: payload,
	}
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

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
