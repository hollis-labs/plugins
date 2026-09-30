package ghplugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// sentinelToken is distinctive, and carries characters that URL encoding
// changes, so a leak in either form is unambiguous.
const sentinelToken = "SENTINEL-GITHUB-TOKEN+/=6d2a90e1"

func pluginWithToken(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := &Plugin{newBackend: func(string) Backend { return backend }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretToken: sentinelToken}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

// A credential this plugin was handed must not leave it in any error text,
// including text net/http composed that no redaction pattern could recognise.
func TestErrorsNeverCarryTheToken(t *testing.T) {
	leaky := &fakeBackend{err: fmt.Errorf(
		`GET "https://api.github.com/repos/o/r?access_token=%s": 403 Forbidden {"echo":"%s","header":"Bearer %s"}`,
		url.QueryEscape(sentinelToken), sentinelToken, sentinelToken)}
	p := pluginWithToken(t, leaky)

	for _, op := range Definition().Operations {
		_, err := callTool(p, op.Name, validArgs(op.Name))
		if err == nil {
			t.Fatalf("%s: want the backend error", op.Name)
		}
		for _, form := range []string{sentinelToken, url.QueryEscape(sentinelToken), url.PathEscape(sentinelToken)} {
			if strings.Contains(err.Error(), form) {
				t.Fatalf("%s leaked the token (%q) in:\n%s", op.Name, form, err)
			}
		}
		if !strings.Contains(err.Error(), redactedMarker) {
			t.Fatalf("%s: want the marker where the token was:\n%s", op.Name, err)
		}
		if !strings.Contains(err.Error(), "403 Forbidden") {
			t.Fatalf("%s: scrubbing removed more than the token:\n%s", op.Name, err)
		}
	}
}

// End to end through the real client: GitHub (here a fake) echoes the bearer
// token in a 401 body, unlabelled, and the coded result the plugin returns
// does not carry it.
func TestAnEchoedTokenNeverLeavesThroughTheClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, `{"message":"Bad credentials for %s"}`, token)
	}))
	t.Cleanup(server.Close)
	p := &Plugin{newBackend: func(token string) Backend {
		c := NewClient(token)
		c.baseURL = server.URL
		return c
	}}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretToken: sentinelToken}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := callTool(p, "status", validArgs(""))
	code, message := failure(t, result, err)
	if code != "credential_missing" {
		t.Fatalf("code = %q (%s)", code, message)
	}
	if strings.Contains(string(result.Content), sentinelToken) || !strings.Contains(message, redactedMarker) {
		t.Fatalf("result = %s", result.Content)
	}
}

func TestScrubberLeavesTextWithoutTheValueAlone(t *testing.T) {
	s := newScrubber(sentinelToken)
	const text = "list releases o/r: API returned 403: rate limited"
	if got := s.text(text); got != text {
		t.Fatalf("text = %q, want it unchanged", got)
	}
	if s.err(nil) != nil {
		t.Fatal("err(nil) must stay nil")
	}
}

func TestScrubberIgnoresDegenerateValues(t *testing.T) {
	s := newScrubber("", "a")
	if got := s.text("a repo named a"); got != "a repo named a" {
		t.Fatalf("a one-character value scrubbed ordinary text: %q", got)
	}
	if got := s.err(errors.New("x")).Error(); got != "x" {
		t.Fatalf("err = %q", got)
	}
}
