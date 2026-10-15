package hadron

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fake is a hadrond stand-in: the first inspects report running, then succeeded.
type fake struct {
	t        *testing.T
	runsSeen atomic.Int32
	inspects atomic.Int32
	start    func(w http.ResponseWriter, body map[string]any)
	lastAuth string
}

func (f *fake) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case r.URL.Path == "/v1/workflows/runs":
			f.runsSeen.Add(1)
			f.start(w, body)
		case strings.HasSuffix(r.URL.Path, "/inspect"):
			n := f.inspects.Add(1)
			status := "running"
			if n >= 2 {
				status = "succeeded"
			}
			if d, _ := body["display"].(map[string]any); d["private"] != "reveal" {
				f.t.Errorf("inspect must ask to reveal private outputs, got %v", body["display"])
			}
			_, _ = w.Write([]byte(`{"run":{"status":"` + status + `"},"values":[
			  {"roles":["run.inputs"],"values":{"id":{"payload":"x"}}},
			  {"roles":["event.3","run.outputs"],"values":{"svg":{"payload":"<svg/>"},"sprite":{"payload":{"hue":7}}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func okStart(w http.ResponseWriter, _ map[string]any) {
	_, _ = w.Write([]byte(`{"run":{"id":"r1","status":"running"}}`))
}

func client(url string) *Client {
	return &Client{
		BaseURL: url, Locator: "/w/avatar-generate.workflow.yaml",
		Token:     func() (string, error) { return "tok", nil },
		PollEvery: time.Millisecond, Timeout: 2 * time.Second,
	}
}

func TestRunWaitsAndReadsOutputs(t *testing.T) {
	f := &fake{t: t, start: okStart}
	srv := f.server()
	defer srv.Close()
	out, err := client(srv.URL).Run(context.Background(), "r1", map[string]any{"id": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out["svg"]) != `"<svg/>"` {
		t.Errorf("svg = %s", out["svg"])
	}
	if f.lastAuth != "Bearer tok" {
		t.Errorf("auth header = %q", f.lastAuth)
	}
}

func TestRefusalIsAnErrorWithTheDiagnostics(t *testing.T) {
	f := &fake{t: t, start: func(w http.ResponseWriter, _ map[string]any) {
		_, _ = w.Write([]byte(`{"diagnostics":[{"code":"HADR-VALUE-015","message":"workflow input \"fill\" does not satisfy its schema"}]}`))
	}}
	srv := f.server()
	defer srv.Close()
	_, err := client(srv.URL).Run(context.Background(), "r1", nil)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "HADR-VALUE-015") {
		t.Fatalf("got %v", err)
	}
}

func TestConflictAndUnavailableAreDistinct(t *testing.T) {
	cases := map[int]error{http.StatusConflict: ErrConflict, http.StatusServiceUnavailable: ErrUnavailable, http.StatusInternalServerError: ErrUnavailable}
	for code, want := range cases {
		f := &fake{t: t, start: func(w http.ResponseWriter, _ map[string]any) { w.WriteHeader(code) }}
		srv := f.server()
		if _, err := client(srv.URL).Run(context.Background(), "r1", nil); !errors.Is(err, want) {
			t.Errorf("HTTP %d: got %v, want %v", code, err, want)
		}
		srv.Close()
	}
}

func TestA500FromAHealthyDaemonIsNotRetryable(t *testing.T) {
	healthy := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal_error"}`))
	}))
	defer srv.Close()
	_, err := client(srv.URL).Run(context.Background(), "r1", nil)
	if !errors.Is(err, ErrServer) || errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "/w/avatar-generate.workflow.yaml") {
		t.Fatalf("healthy daemon: got %v, want ErrServer naming the workflow path", err)
	}
	healthy = false
	if _, err = client(srv.URL).Run(context.Background(), "r1", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("not-ready daemon: got %v, want ErrUnavailable", err)
	}
}

func TestUnreadableTokenIsAConfigError(t *testing.T) {
	c := client("http://127.0.0.1:1")
	c.Token = TokenFile("/nonexistent/token")
	if _, err := c.Run(context.Background(), "r1", nil); !errors.Is(err, ErrConfig) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreachableDaemonIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := client(url).Run(context.Background(), "r1", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestFailedRunIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/inspect") {
			_, _ = w.Write([]byte(`{"run":{"status":"failed"},"values":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"run":{"status":"running"}}`))
	}))
	defer srv.Close()
	if _, err := client(srv.URL).Run(context.Background(), "r1", nil); !errors.Is(err, ErrRunFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestSlowRunTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"run":{"status":"running"}}`))
	}))
	defer srv.Close()
	c := client(srv.URL)
	c.Timeout = 30 * time.Millisecond
	if _, err := c.Run(context.Background(), "r1", nil); !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v", err)
	}
}
