package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	re "github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
	"github.com/hollis-labs/plugins/activity-monitor/internal/scan"
)

type activityBatch struct {
	ID         string     `json:"id"`
	ObservedAt time.Time  `json:"observed_at"`
	Events     []re.Event `json:"events"`
}
type activityReport struct {
	current    bool
	WorkerID   string         `json:"worker_id"`
	Machine    string         `json:"machine"`
	Connected  bool           `json:"connected"`
	ReceivedAt time.Time      `json:"received_at"`
	Activity   *activityBatch `json:"activity"`
}
type activityState struct {
	State        string                     `json:"state"`
	LastActivity *time.Time                 `json:"last_activity"`
	LastAction   json.RawMessage            `json:"last_action"`
	Stale        bool                       `json:"stale"`
	Confidence   map[string]scan.Confidence `json:"confidence"`
}

type fleet struct {
	mu                           sync.Mutex
	reports                      map[string]activityReport
	err                          string
	lastFetch                    time.Time
	url, tokenFile, localMachine string
	client                       *http.Client
}

func newFleet(config map[string]string) (*fleet, error) {
	f := &fleet{reports: map[string]activityReport{}, tokenFile: config["tether_token_file"], localMachine: config["machine"]}
	if f.localMachine == "" {
		f.localMachine, _ = os.Hostname()
		f.localMachine = strings.SplitN(f.localMachine, ".", 2)[0]
	}
	f.client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := strings.TrimRight(config["tether_url"], "/")
	if socket := config["tether_socket"]; socket != "" {
		if base != "" || f.tokenFile != "" {
			return nil, fmt.Errorf("choose tether_socket or tether_url with tether_token_file")
		}
		f.client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}
		base = "http://tether"
	} else if base != "" {
		u, err := url.Parse(base)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || f.tokenFile == "" {
			return nil, fmt.Errorf("tether_url requires HTTPS and tether_token_file")
		}
	}
	if base != "" {
		f.url = base + "/workers/activity"
	}
	return f, nil
}

func (f *fleet) fetch(ctx context.Context, now time.Time) {
	if f.url == "" {
		return
	}
	reports, err := f.read(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		f.err = err.Error()
		return
	}
	f.err = ""
	f.lastFetch = now
	for id, r := range f.reports {
		r.Connected = false
		r.current = false
		f.reports[id] = r
	}
	for _, r := range reports {
		if r.Activity == nil {
			old := f.reports[r.WorkerID]
			old.WorkerID, old.Machine, old.Connected = r.WorkerID, r.Machine, r.Connected
			old.current = false
			f.reports[r.WorkerID] = old
			continue
		}
		r.current = true
		f.reports[r.WorkerID] = r
	}
}

func (f *fleet) read(ctx context.Context) ([]activityReport, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", f.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Tether-Protocol", "1")
	if f.tokenFile != "" {
		b, err := os.ReadFile(f.tokenFile)
		if err != nil {
			return nil, fmt.Errorf("activity credential unavailable")
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return nil, fmt.Errorf("empty activity credential")
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("activity hub unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("activity hub returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 4<<20 {
		return nil, fmt.Errorf("activity response too large")
	}
	var body struct {
		Reports []activityReport `json:"reports"`
	}
	if json.Unmarshal(b, &body) != nil || body.Reports == nil {
		return nil, fmt.Errorf("invalid activity response")
	}
	seen := map[string]bool{}
	for _, r := range body.Reports {
		if r.WorkerID == "" || seen[r.WorkerID] {
			return nil, fmt.Errorf("invalid activity machine")
		}
		seen[r.WorkerID] = true
		if r.Activity == nil {
			continue
		}
		if r.Activity.ID == "" || r.ReceivedAt.IsZero() || len(r.Activity.Events) > 512 {
			return nil, fmt.Errorf("invalid activity batch")
		}
		for _, e := range r.Activity.Events {
			if e.Validate() != nil || e.Kind != re.KindSessionHeartbeat || e.SessionID != r.WorkerID+":"+e.Process.Provider+":"+e.Process.ProviderSessionID {
				return nil, fmt.Errorf("invalid activity event")
			}
			var payload activityState
			if json.Unmarshal(e.Payload, &payload) != nil || payload.LastActivity == nil {
				return nil, fmt.Errorf("invalid activity state")
			}
			switch payload.State {
			case "busy", "idle", "waiting", "unknown":
			default:
				return nil, fmt.Errorf("invalid activity state")
			}
		}
	}
	return body.Reports, nil
}

// merge keeps the last source observation during transport loss, explicitly
// stale. A successful empty batch clears a machine; an outage never means exit.
func (f *fleet) merge(local []scan.Agent, now time.Time) []scan.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []scan.Agent{}
	localSessions := map[string]bool{}
	localPIDs := map[string]bool{}
	for _, r := range f.reports {
		if r.Activity == nil {
			continue
		}
		lost := !r.current || !r.Connected || f.err != "" || now.Sub(r.ReceivedAt) > 30*time.Second || now.Sub(f.lastFetch) > 30*time.Second
		for _, e := range r.Activity.Events {
			var p activityState
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if p.Confidence == nil {
				p.Confidence = map[string]scan.Confidence{}
			}
			stale := lost || p.Stale
			if p.LastActivity == nil || now.Sub(*p.LastActivity) > 5*time.Minute {
				stale = true
			}
			if stale {
				p.State = "unknown"
				p.Confidence["state"] = scan.Derived
			}
			a := scan.Agent{ID: e.SessionID, Machine: r.Machine, Provider: e.Process.Provider, PID: e.Process.PID, SessionID: e.Process.ProviderSessionID, State: p.State, LastActivity: p.LastActivity, LastAction: p.LastAction, Stale: stale, Source: "pulse", Conf: p.Confidence}
			// Add local cwd only from local evidence, never from a remote PID.
			if r.Machine == f.localMachine {
				localSessions[a.Provider+":"+a.SessionID] = true
				localPIDs[fmt.Sprintf("%s:%d", a.Provider, a.PID)] = true
				for _, l := range local {
					if l.Provider == a.Provider && (l.SessionID == a.SessionID || l.SessionID == "" && l.PID == a.PID) {
						a.Cwd = l.Cwd
					}
				}
			}
			out = append(out, a)
		}
	}
	for _, a := range local {
		if a.SessionID != "" && localSessions[a.Provider+":"+a.SessionID] || a.SessionID == "" && localPIDs[fmt.Sprintf("%s:%d", a.Provider, a.PID)] {
			continue
		}
		a.Machine = f.localMachine
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *fleet) status() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	machines := []map[string]any{}
	for _, r := range f.reports {
		machines = append(machines, map[string]any{"worker_id": r.WorkerID, "machine": r.Machine, "connected": r.Connected, "reporting": r.current && r.Activity != nil, "received_at": r.ReceivedAt})
	}
	return map[string]any{"configured": f.url != "", "last_fetch": f.lastFetch, "error": f.err, "machines": machines}
}
