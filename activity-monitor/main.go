// Command activity-monitor reports which coding agents are alive on this
// machine and what they are doing. With no arguments it serves as a
// plugin-sdk subprocess plugin (http/handle routes); `scan` prints one
// snapshot and exits.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/capability"
	sdksub "github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"

	"github.com/hollis-labs/plugins/activity-monitor/internal/scan"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "scan" {
		agents, _ := scan.New().Scan(context.Background())
		if agents == nil {
			agents = []scan.Agent{}
		}
		enc := json.NewEncoder(os.Stdout)
		if len(os.Args) > 2 && os.Args[2] == "--json" {
			enc.SetIndent("", "  ")
			_ = enc.Encode(agents)
			return
		}
		for _, a := range agents {
			fmt.Printf("%-7s pid=%-8d %-8s %s\n", a.Provider, a.PID, a.State, a.Cwd)
		}
		return
	}
	p := &plugin{sc: scan.New(), tr: scan.NewTracker(2000), push: &pusher{}}

	if err := sdksub.Serve(p); err != nil {
		os.Exit(1)
	}
}

type plugin struct {
	fleet  *fleet
	cancel context.CancelFunc
	done   chan struct{}
	sc     *scan.Scanner
	tr     *scan.Tracker
	push   *pusher
}

func (p *plugin) poll(ctx context.Context, every time.Duration) {
	for {
		now := time.Now()
		p.fleet.fetch(ctx, now)
		if agents, err := p.sc.Scan(ctx); err == nil {
			p.push.send(ctx, p.tr.Update(now, p.fleet.merge(agents, now)))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (p *plugin) Init(ctx context.Context, params sdksub.InitParams) (sdksub.InitResult, error) {
	var err error
	p.fleet, err = newFleet(params.Config)
	if err != nil {
		return sdksub.InitResult{}, err
	}
	p.push.configure(params.Config["ingest_url"], params.Config["ingest_token"])
	return sdksub.InitResult{ID: "activity-monitor", Name: "Activity Monitor", Version: "0.1.0",
		Description: "Live coding-agent activity on this machine.", Protocol: sdksub.ProtocolVersion,
		CapabilityContract: capability.ContractVersion}, nil
}
func (p *plugin) Load(context.Context) (sdksub.LoadResult, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	done := make(chan struct{})
	p.done = done
	go func() { defer close(done); p.poll(ctx, 2*time.Second) }()
	return sdksub.LoadResult{}, nil
}
func (p *plugin) Unload(ctx context.Context) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *plugin) Health(context.Context) (sdksub.HealthStatus, error) {
	return sdksub.HealthStatus{OK: true}, nil
}

// HTTPHandle serves paths relative to the plugin mount: /agents, /events.
func (p *plugin) HTTPHandle(ctx context.Context, r sdksub.HTTPRequest) (sdksub.HTTPResponse, error) {
	if r.Method != "GET" {
		return jsonResp(405, map[string]string{"error": "method not allowed"}), nil
	}
	path := strings.TrimSuffix(r.Path, "/")
	switch {
	case strings.HasSuffix(path, "/agents"):
		return jsonResp(200, map[string]any{"agents": p.tr.Agents(), "transport": p.fleet.status()}), nil
	case strings.HasSuffix(path, "/events"):
		after, _ := strconv.ParseUint(r.Query["since"], 10, 64)
		evs, latest := p.tr.Since(after)
		return jsonResp(200, map[string]any{"events": evs, "latest": latest}), nil
	}
	return jsonResp(404, map[string]string{"error": "not found"}), nil
}

func jsonResp(status int, v any) sdksub.HTTPResponse {
	b, _ := json.Marshal(v)
	return sdksub.HTTPResponse{Status: status, Headers: map[string]string{"Content-Type": "application/json"}, Body: b}
}
