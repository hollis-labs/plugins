// Package hadron is the avatar plugin's client for hadrond's workflow HTTP API.
// It starts one run of the avatar workflow, waits for it to finish and reads its
// outputs. It is deliberately narrow: start, inspect, nothing else.
//
// The shapes here are the daemon's (apps/hadron internal/appworkflow
// RunWorkflowRequest / InspectWorkflowRunRequest), checked against a live
// hadrond rather than inferred from the structs.
package hadron

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is where hadrond listens by default.
	DefaultBaseURL = "http://127.0.0.1:8095"

	maxResponseBytes = 8 << 20
	outputsRole      = "run.outputs"
)

var (
	// ErrUnavailable: hadrond could not be reached, or answered not-ready.
	ErrUnavailable = errors.New("hadron: unavailable")
	// ErrConfig: the plugin cannot authenticate, e.g. the token file is unreadable.
	// Retrying will not help; the operator has to fix the configuration.
	ErrConfig = errors.New("hadron: misconfigured")
	// ErrServer: hadrond is healthy but answered this request with a 500. Most
	// often the workflow file is missing or outside its source root, which
	// hadrond reports as a bare internal_error rather than a diagnostic.
	ErrServer = errors.New("hadron: request failed")
	// ErrRejected: the run was not admitted (bad input or definition).
	ErrRejected = errors.New("hadron: run not admitted")
	// ErrConflict: the run id exists with different inputs.
	ErrConflict = errors.New("hadron: run id conflict")
	// ErrRunFailed: the run was admitted and ended unsuccessfully.
	ErrRunFailed = errors.New("hadron: run failed")
	// ErrTimeout: the run did not finish within the client's wait.
	ErrTimeout = errors.New("hadron: run did not finish in time")
)

// Client talks to one hadrond for one workflow definition.
type Client struct {
	BaseURL string
	// Token returns the operator bearer token. It is called per request so a
	// rotated token (`hadron auth rotate`) is picked up without a restart.
	Token func() (string, error)
	// Locator is the workflow file, as hadrond sees it: it must lie under the
	// daemon's workflow source root (<data dir>/workflows).
	Locator string
	HTTP    *http.Client
	// PollEvery and Timeout bound the wait for a run to finish.
	PollEvery time.Duration
	Timeout   time.Duration
}

// TokenFile reads a bearer token from path on every call.
func TokenFile(path string) func() (string, error) {
	return func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%w: reading operator token: %v", ErrConfig, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// Outputs are a finished run's named values, as raw JSON.
type Outputs map[string]json.RawMessage

// Run starts runID with inputs (or replays it if it already exists with the
// same inputs) and returns its outputs once it has succeeded.
func (c *Client) Run(ctx context.Context, runID string, inputs map[string]any) (Outputs, error) {
	timeout, every := c.Timeout, c.PollEvery
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if every <= 0 {
		every = 100 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	status, err := c.start(ctx, runID, inputs)
	if err != nil {
		return nil, err
	}
	for {
		switch status {
		case "succeeded":
			return c.outputs(ctx, runID)
		case "running", "pending", "":
		default:
			return nil, fmt.Errorf("%w: run %s ended %s", ErrRunFailed, runID, status)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: run %s", ErrTimeout, runID)
		case <-time.After(every):
		}
		if status, err = c.status(ctx, runID); err != nil {
			return nil, err
		}
	}
}

type startResponse struct {
	Run *struct {
		Status string `json:"status"`
	} `json:"run"`
	Diagnostics []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"diagnostics"`
}

func (c *Client) start(ctx context.Context, runID string, inputs map[string]any) (string, error) {
	body := map[string]any{
		"run_id":          runID,
		"idempotency_key": runID,
		"definition":      map[string]any{"kind": "workflow", "locator": c.Locator},
		"inputs":          inputs,
		"identity":        map[string]any{"source_authority": "http"},
	}
	var out startResponse
	code, raw, err := c.post(ctx, "/v1/workflows/runs", body, &out)
	if err != nil {
		return "", err
	}
	switch {
	case code == http.StatusConflict:
		return "", fmt.Errorf("%w: %s", ErrConflict, runID)
	case code >= 500:
		return "", c.serverFailure(ctx, code, raw)
	case code != http.StatusOK:
		return "", fmt.Errorf("hadron: starting %s: HTTP %d: %s", runID, code, snippet(raw))
	}
	if out.Run == nil {
		// A refusal is a 200 with diagnostics and no run.
		var msgs []string
		for _, d := range out.Diagnostics {
			msgs = append(msgs, d.Code+": "+d.Message)
		}
		if len(msgs) == 0 {
			msgs = append(msgs, snippet(raw))
		}
		return "", fmt.Errorf("%w: %s", ErrRejected, strings.Join(msgs, "; "))
	}
	return out.Run.Status, nil
}

type inspectResponse struct {
	Run struct {
		Status string `json:"status"`
	} `json:"run"`
	Values []struct {
		Roles  []string `json:"roles"`
		Values map[string]struct {
			Payload json.RawMessage `json:"payload"`
		} `json:"values"`
	} `json:"values"`
}

func (c *Client) inspect(ctx context.Context, runID string) (*inspectResponse, error) {
	body := map[string]any{
		"run_id":      runID,
		"identity":    map[string]any{"source_authority": "http"},
		"display":     map[string]any{"private": "reveal"}, // outputs are private by default
		"value_limit": 50,
	}
	var out inspectResponse
	code, raw, err := c.post(ctx, "/v1/workflows/runs/"+url.PathEscape(runID)+"/inspect", body, &out)
	if err != nil {
		return nil, err
	}
	if code >= 500 {
		return nil, c.serverFailure(ctx, code, raw)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("hadron: inspecting %s: HTTP %d: %s", runID, code, snippet(raw))
	}
	return &out, nil
}

func (c *Client) status(ctx context.Context, runID string) (string, error) {
	r, err := c.inspect(ctx, runID)
	if err != nil {
		return "", err
	}
	return r.Run.Status, nil
}

func (c *Client) outputs(ctx context.Context, runID string) (Outputs, error) {
	r, err := c.inspect(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, v := range r.Values {
		for _, role := range v.Roles {
			if role != outputsRole {
				continue
			}
			out := Outputs{}
			for name, val := range v.Values {
				out[name] = val.Payload
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("hadron: run %s succeeded but has no %s values", runID, outputsRole)
}

// serverFailure classifies a 5xx. A daemon that is not ready (or not answering)
// is unavailable and worth retrying; one that reports ready but still failed
// this request has a problem with the request, which a retry will not fix.
func (c *Client) serverFailure(ctx context.Context, code int, raw []byte) error {
	if !c.ready(ctx) {
		return fmt.Errorf("%w: HTTP %d: %s", ErrUnavailable, code, snippet(raw))
	}
	return fmt.Errorf("%w: hadrond is ready but answered HTTP %d: %s; check that %s exists under hadrond's workflow source root", ErrServer, code, snippet(raw), c.Locator)
}

func (c *Client) ready(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/v1/health", nil)
	if err != nil {
		return false
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK
}

func (c *Client) post(ctx context.Context, path string, body any, into any) (int, []byte, error) {
	token, err := c.Token()
	if err != nil {
		return 0, nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
		}
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: reading response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode == http.StatusOK && into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			return 0, nil, fmt.Errorf("hadron: decoding %s response: %w", path, err)
		}
	}
	return resp.StatusCode, raw, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
