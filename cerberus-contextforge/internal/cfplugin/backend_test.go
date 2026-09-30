package cfplugin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// closedLoopbackAddress returns a loopback URL with nothing listening on it,
// which is exactly what a down tunnel looks like from here.
func closedLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	address := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return address
}

// The failure this guards against is an operator being sent to the wrong host.
// A refused connection on the local tunnel port means the tunnel is down; the
// gateway on its host may be perfectly healthy.
func TestRefusedLoopbackConnectionBlamesTheTunnel(t *testing.T) {
	backend, err := NewSDKBackend(closedLoopbackAddress(t), "token")
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}

	_, err = backend.ListGateways(context.Background())
	if err == nil {
		t.Fatal("expected an error against a closed port")
	}
	message := err.Error()
	if !strings.Contains(message, "tunnel is down") {
		t.Fatalf("error should name the tunnel, got: %v", message)
	}
	if !strings.Contains(message, "cerberus resource start") {
		t.Fatalf("error should name the resource that fixes it, got: %v", message)
	}
	if strings.Contains(strings.ToLower(message), "gateway is down") {
		t.Fatalf("error blames the gateway for a tunnel failure: %v", message)
	}
}

// A non-loopback address is a real remote gateway, so the same refusal must not
// be reported as a tunnel problem.
func TestRefusedRemoteConnectionDoesNotBlameTheTunnel(t *testing.T) {
	backend, err := NewSDKBackend("http://192.0.2.1:4444", "token")
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // fail fast rather than waiting on an unroutable TEST-NET address

	if _, err := backend.ListGateways(ctx); err != nil {
		if strings.Contains(err.Error(), "tunnel is down") {
			t.Fatalf("a remote address must not be reported as a tunnel failure: %v", err)
		}
	}
}

func TestGetHealthReadsTheOpenEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	backend, err := NewSDKBackend(server.URL, "")
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	health, err := backend.GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !health.OK || health.Status != "ok" {
		t.Fatalf("health = %+v", health)
	}
	if health.Address != server.URL {
		t.Fatalf("Address = %q, want %q", health.Address, server.URL)
	}
}

func TestGetHealthReportsNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	backend, _ := NewSDKBackend(server.URL, "")
	health, err := backend.GetHealth(context.Background())
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if health.OK || !strings.Contains(health.Status, "503") {
		t.Fatalf("health = %+v, want a not-ok status naming 503", health)
	}
}

// An empty address must fall back to the tunnel rather than producing a client
// pointed at nothing.
func TestEmptyAddressFallsBackToTheTunnel(t *testing.T) {
	backend, err := NewSDKBackend("", "")
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	if got := backend.(*sdkBackend).address; got != DefaultAddress {
		t.Fatalf("address = %q, want %q", got, DefaultAddress)
	}
}

// A missing token must not prevent construction: /health is open, and the
// auth'd operations report a 401 that explains how to supply one.
func TestBackendBuildsWithoutAToken(t *testing.T) {
	if _, err := NewSDKBackend(DefaultAddress, ""); err != nil {
		t.Fatalf("NewSDKBackend without a token: %v", err)
	}
}

// A 401 must tell the operator how to supply a token. ContextForge accepts
// bearer JWTs only, so "unauthorized" alone sends people to try an API key.
func TestUnauthorizedNamesTheSecretStore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	backend, _ := NewSDKBackend(server.URL, "")
	_, err := backend.ListGateways(context.Background())
	if err == nil {
		t.Fatal("expected a 401 error")
	}
	message := err.Error()
	for _, want := range []string{"unauthorized", "requires a JWT", keychainKey, TokenEnvVar} {
		if !strings.Contains(message, want) {
			t.Fatalf("401 error should mention %q, got: %v", want, message)
		}
	}

	// The host redacts /(?i)\bBearer[ \t]+\S+/ on every error path, so a
	// message containing that pattern arrives at the operator garbled — the
	// guidance would be destroyed by the very safety net meant to protect it.
	if regexp.MustCompile(`(?i)\bBearer[ \t]+[a-z0-9._~+/=-]+`).MatchString(message) {
		t.Fatalf("401 guidance will be mangled by host redaction: %v", message)
	}
}

// The token must never appear in an error, however the request failed.
func TestErrorsNeverCarryTheToken(t *testing.T) {
	const token = "SENTINEL-JWT-VALUE-5c7e"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	backend, _ := NewSDKBackend(server.URL, token)
	if _, err := backend.ListGateways(context.Background()); err != nil {
		if strings.Contains(err.Error(), token) {
			t.Fatalf("error leaked the bearer token: %v", err)
		}
	}
}
