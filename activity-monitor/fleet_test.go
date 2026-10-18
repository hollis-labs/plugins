package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	re "github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/plugins/activity-monitor/internal/scan"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func reportFor(t *testing.T, worker, machine, sid string, now time.Time) activityReport {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"state": "busy", "last_activity": now, "last_action": map[string]any{"tool": "Read", "done": true}, "confidence": map[string]string{"state": "exact"}})
	return activityReport{current: true, WorkerID: worker, Machine: machine, Connected: true, ReceivedAt: now, Activity: &activityBatch{ID: "batch-" + sid, ObservedAt: now, Events: []re.Event{{SchemaVersion: re.SchemaVersion, ID: worker + "-" + sid, Kind: re.KindSessionHeartbeat, Time: now, Sequence: 1, SessionID: worker + ":codex:" + sid, Process: re.Process{PID: 42, Provider: "codex", ProviderSessionID: sid}, Source: re.Source{Channel: "plugin-hook"}, Payload: payload}}}}
}

func TestFleetPreservesThreadsAndMachineIdentity(t *testing.T) {
	now := time.Now().UTC()
	f, _ := newFleet(map[string]string{"machine": "local"})
	a := reportFor(t, "w1", "local", "one", now)
	b := reportFor(t, "w1", "local", "two", now)
	a.Activity.Events = append(a.Activity.Events, b.Activity.Events...)
	f.reports["w1"] = a
	f.reports["w2"] = reportFor(t, "w2", "remote", "one", now)
	f.lastFetch = now
	local := []scan.Agent{{ID: "local-process", Provider: "codex", PID: 42, Cwd: "/local/project", State: "unknown"}}
	agents := f.merge(local, now)
	got := map[string]scan.Agent{}
	for _, a := range agents {
		got[a.ID] = a
	}
	for _, id := range []string{"w1:codex:one", "w1:codex:two", "w2:codex:one"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("lost %s: %+v", id, agents)
		}
	}
	if _, ok := got["local-process"]; ok {
		t.Fatal("pulse did not replace process-only local observation")
	}
	if got["w2:codex:one"].Cwd != "" || got["w1:codex:one"].Cwd != "/local/project" {
		t.Fatal("joined a remote PID to local process data")
	}
	for _, a := range f.merge(local, now.Add(time.Minute)) {
		if a.State != "unknown" || !a.Stale {
			t.Fatalf("transport outage looked live: %+v", a)
		}
	}
}

func TestFleetFailureKeepsStaleAndEmptySuccessClears(t *testing.T) {
	now := time.Now().UTC()
	f, _ := newFleet(map[string]string{"tether_socket": "/test/socket"})
	report := reportFor(t, "worker", "machine", "session", now)
	body, _ := json.Marshal(map[string]any{"reports": []activityReport{report}})
	status := 200
	f.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	f.fetch(context.Background(), now)
	if got := f.merge(nil, now); len(got) != 1 || got[0].State != "busy" || got[0].Stale {
		t.Fatalf("did not ingest observation: %+v", got)
	}
	status = 503
	f.fetch(context.Background(), now.Add(time.Second))
	if got := f.merge(nil, now.Add(time.Second)); len(got) != 1 || !got[0].Stale || got[0].State != "unknown" {
		t.Fatalf("outage was mistaken for exit: %+v", got)
	}
	status = 200
	body = []byte(`{"reports":[{"worker_id":"worker","machine":"machine","connected":false}]}`)
	f.fetch(context.Background(), now.Add(2*time.Second))
	if got := f.merge(nil, now.Add(2*time.Second)); len(got) != 1 || !got[0].Stale {
		t.Fatalf("disconnected worker disappeared: %+v", got)
	}
	report.Activity.Events = []re.Event{}
	body, _ = json.Marshal(map[string]any{"reports": []activityReport{report}})
	f.fetch(context.Background(), now.Add(3*time.Second))
	if got := f.merge(nil, now.Add(3*time.Second)); len(got) != 0 {
		t.Fatalf("successful empty source scan did not clear: %+v", got)
	}
}

func TestFleetRejectsInvalidBatchWithoutPartialReplacement(t *testing.T) {
	now := time.Now().UTC()
	f, _ := newFleet(map[string]string{"tether_socket": "/test/socket"})
	f.reports["w"] = reportFor(t, "w", "machine", "good", now)
	bad := reportFor(t, "w", "machine", "bad", now)
	bad.Activity.Events[0].SessionID = "another-worker:codex:bad"
	body, _ := json.Marshal(map[string]any{"reports": []activityReport{bad}})
	f.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	f.fetch(context.Background(), now)
	got := f.merge(nil, now)
	if len(got) != 1 || got[0].SessionID != "good" || !got[0].Stale {
		t.Fatalf("bad response replaced prior evidence: %+v", got)
	}
}
