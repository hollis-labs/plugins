package cfplugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func callCoded(t *testing.T, backend Backend, operation string) (cerbplugin.ErrorCode, string) {
	t.Helper()
	result, err := NewWithBackend(backend).MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, operation),
	})
	if err != nil {
		t.Fatalf("%s returned an uncoded error, want a coded tool result: %v", operation, err)
	}
	if !result.IsError {
		t.Fatalf("%s succeeded, want a coded failure", operation)
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !ok {
		t.Fatalf("%s error result is not coded: %s", operation, result.Content)
	}
	return code, message
}

// The case the code exists for: get_health with the tunnel down is
// unreachable, never credential_missing, even though the host knows the JWT
// is absent. /health needs no token.
func TestGetHealthWithTheTunnelDownIsCodedUnavailable(t *testing.T) {
	backend, err := NewSDKBackend(closedLoopbackAddress(t), "")
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	code, message := callCoded(t, backend, "get_health")
	if code != cerbplugin.ErrorUnavailable {
		t.Fatalf("code = %q, want %q", code, cerbplugin.ErrorUnavailable)
	}
	if !strings.Contains(message, "the tunnel is down, not the gateway") {
		t.Fatalf("the tunnel diagnosis was lost: %s", message)
	}
}

func TestUnauthorizedIsCodedCredentialMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	backend, _ := NewSDKBackend(server.URL, "")
	code, message := callCoded(t, backend, "list_gateways")
	if code != cerbplugin.ErrorCredentialMissing {
		t.Fatalf("code = %q, want %q", code, cerbplugin.ErrorCredentialMissing)
	}
	if !strings.Contains(message, "requires a JWT") {
		t.Fatalf("the 401 guidance was lost: %s", message)
	}
}

// A gateway that answered with a server error is not unreachable, so it stays
// uncoded and the host reports it as operation_failed.
func TestServerErrorIsNotCodedUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	backend, _ := NewSDKBackend(server.URL, "jwt")
	_, err := NewWithBackend(backend).MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "list_gateways"),
	})
	if err == nil {
		t.Fatal("a 500 must fail")
	}
}
