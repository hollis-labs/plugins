package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	re "github.com/hollis-labs/libs/harness/core/adapters/runtimeevents"
)

const maxPending = 2000

// pusher posts events to the host's ingest endpoint as they are produced, so
// consumers get them over the host's SSE stream without polling. Until the
// host hands over an ingest URL (standalone/CLI use) it only buffers.
// Failed batches are kept (bounded) and retried with the next batch.
type pusher struct {
	mu      sync.Mutex
	url     string
	token   string
	pending []re.Event
	client  http.Client
}

func (p *pusher) configure(url, token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.url, p.token = url, token
	p.client.Timeout = 3 * time.Second
}

func (p *pusher) send(ctx context.Context, evs []re.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = append(p.pending, evs...)
	if over := len(p.pending) - maxPending; over > 0 {
		p.pending = append([]re.Event(nil), p.pending[over:]...)
	}
	if p.url == "" || len(p.pending) == 0 {
		return
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, e := range p.pending {
		_ = enc.Encode(e)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, &body)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := p.client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			p.pending = nil
			return
		}
		// A request the host will never accept must not wedge the queue.
		if c := resp.StatusCode; c >= 400 && c < 500 && c != http.StatusRequestTimeout && c != http.StatusTooManyRequests {
			p.pending = nil
		}
		err = fmt.Errorf("ingest returned %s", resp.Status)
	}
	fmt.Fprintln(os.Stderr, "activity-monitor: push failed, will retry:", err)
}
