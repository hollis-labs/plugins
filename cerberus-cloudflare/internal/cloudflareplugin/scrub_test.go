package cloudflareplugin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// sentinelToken is distinctive, and carries characters that URL encoding
// changes, so a leak in either form is unambiguous.
const sentinelToken = "SENTINEL-CF-TOKEN+/=9f2c7b41"

// A credential this plugin was handed must not leave it in any error text,
// including text an SDK composed that no redaction pattern could recognise.
func TestErrorsNeverCarryTheToken(t *testing.T) {
	leaky := &fakeBackend{err: fmt.Errorf(
		`GET "https://api.cloudflare.com/client/v4/zones?token=%s": 400 Bad Request {"echo":"%s","header":"Bearer %s"}`,
		url.QueryEscape(sentinelToken), sentinelToken, sentinelToken)}
	p := &Plugin{newBackend: func(string) Backend { return leaky }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretAPIToken: sentinelToken}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

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
		if !strings.Contains(err.Error(), "400 Bad Request") {
			t.Fatalf("%s: scrubbing removed more than the token:\n%s", op.Name, err)
		}
	}
}

func TestScrubberLeavesTextWithoutTheValueAlone(t *testing.T) {
	s := newScrubber(sentinelToken)
	const text = "list zones: 403 Forbidden"
	if got := s.text(text); got != text {
		t.Fatalf("text = %q, want it unchanged", got)
	}
	if s.err(nil) != nil {
		t.Fatal("err(nil) must stay nil")
	}
}

func TestScrubberIgnoresDegenerateValues(t *testing.T) {
	s := newScrubber("", "a")
	if got := s.text("a zone named a"); got != "a zone named a" {
		t.Fatalf("a one-character value scrubbed ordinary text: %q", got)
	}
	if got := s.err(errors.New("x")).Error(); got != "x" {
		t.Fatalf("err = %q", got)
	}
}
