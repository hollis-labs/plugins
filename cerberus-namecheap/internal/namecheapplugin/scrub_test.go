package namecheapplugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// sentinelKey is distinctive, and carries characters that URL encoding
// changes, so a leak in either form is unambiguous.
const (
	sentinelKey  = "SENTINEL-NC-KEY+/=4b7e2c91"
	sentinelUser = "SENTINEL-NC-USER"
)

func sentinelPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := &Plugin{newBackend: func(Credentials) Backend { return backend }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{
		SecretAPIUser: sentinelUser, SecretAPIKey: sentinelKey, SecretUsername: sentinelUser,
	}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func assertNoSentinel(t *testing.T, where, text string) {
	t.Helper()
	for _, form := range []string{sentinelKey, url.QueryEscape(sentinelKey), url.PathEscape(sentinelKey), sentinelUser} {
		if strings.Contains(text, form) {
			t.Fatalf("%s leaked %q:\n%s", where, form, text)
		}
	}
}

// The API key travels in the query string, and net/http prints the whole URL
// in a transport error. This is that error, from a refused connection: it is
// coded unavailable, and neither the key in any form nor the user survives.
func TestTransportErrorWithTheKeyInTheURLIsCodedAndScrubbed(t *testing.T) {
	query := url.Values{}
	query.Set("ApiUser", sentinelUser)
	query.Set("ApiKey", sentinelKey)
	query.Set("UserName", sentinelUser)
	query.Set("ClientIp", "127.0.0.1")
	query.Set("Command", "namecheap.domains.getList")
	transport := fmt.Errorf("namecheap list domains: http request: %w", &url.Error{
		Op:  "Get",
		URL: APIBaseURL + "?" + query.Encode(),
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")},
	})
	if !strings.Contains(transport.Error(), url.QueryEscape(sentinelKey)) {
		t.Fatalf("fixture does not carry the escaped key: %v", transport)
	}

	result, err := callTool(sentinelPlugin(t, &fakeBackend{err: transport}), "list_domains", nil)
	if err != nil {
		t.Fatalf("an unreachable API came back uncoded: %v", err)
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !ok || code != cerbplugin.ErrorUnavailable {
		t.Fatalf("result = %s, want connector_unavailable", result.Content)
	}
	assertNoSentinel(t, "coded result", string(result.Content))
	if !strings.Contains(message, "connection refused") || !strings.Contains(message, redactedMarker) {
		t.Fatalf("scrubbing removed more than the credentials, or nothing: %s", message)
	}
}

// Uncoded errors are scrubbed too, including the raw and path-escaped forms.
func TestUncodedErrorsNeverCarryTheKey(t *testing.T) {
	leak := fmt.Errorf("unexpected status 500: echo %s %s %s", sentinelKey, url.PathEscape(sentinelKey), sentinelUser)
	_, err := callTool(sentinelPlugin(t, &fakeBackend{err: leak}), "list_domains", nil)
	if err == nil {
		t.Fatal("want the backend error")
	}
	assertNoSentinel(t, "uncoded error", err.Error())
}
