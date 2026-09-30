package cfplugin

import (
	"encoding/json"
	"strings"
	"testing"

	cf "github.com/leefowlercu/go-contextforge/contextforge"
)

// sentinel values are distinctive so a leak is unambiguous in the serialized
// output rather than a substring of something innocent.
const (
	sentinelToken    = "SENTINEL-AUTH-TOKEN-b3a1"
	sentinelPassword = "SENTINEL-PASSWORD-9f2c"
	sentinelUsername = "SENTINEL-USERNAME-4d8e"
	sentinelHeader   = "SENTINEL-HEADER-VALUE-7c5b"
	sentinelValue    = "SENTINEL-AUTH-VALUE-1a6f"
	sentinelQuery    = "SENTINEL-QUERY-VALUE-2e9d"
	sentinelOAuth    = "SENTINEL-OAUTH-SECRET-8b4a"
)

func fullyPopulatedGateway() *cf.Gateway {
	return &cf.Gateway{
		ID:                  cf.String("gw-1"),
		Name:                "mcp-svc-a",
		URL:                 "http://svc-a-mcp:8000/mcp",
		Description:         cf.String("Svc A MCP server"),
		Transport:           "streamablehttp",
		Enabled:             true,
		Reachable:           true,
		AuthType:            cf.String("bearer"),
		AuthToken:           cf.String(sentinelToken),
		AuthPassword:        cf.String(sentinelPassword),
		AuthUsername:        cf.String(sentinelUsername),
		AuthHeaderKey:       cf.String("Authorization"),
		AuthHeaderValue:     cf.String(sentinelHeader),
		AuthValue:           cf.String(sentinelValue),
		AuthQueryParamValue: cf.String(sentinelQuery),
		AuthHeaders:         []map[string]string{{"X-Secret": sentinelHeader}},
		OAuthConfig:         map[string]any{"client_secret": sentinelOAuth},
	}
}

// This is the test ADR 0003 requires: a gateway carrying every credential the
// vendor type can hold must not serialize any of them.
func TestGatewayDTODoesNotSerializeCredentials(t *testing.T) {
	encoded, err := json.Marshal(GatewayFromSDK(fullyPopulatedGateway()))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)

	for _, secret := range []string{
		sentinelToken, sentinelPassword, sentinelUsername,
		sentinelHeader, sentinelValue, sentinelQuery, sentinelOAuth,
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("serialized gateway leaked %q:\n%s", secret, got)
		}
	}

	// The vendor's JSON field names must not appear either — their presence
	// would mean the SDK struct is being embedded rather than mapped.
	for _, field := range []string{
		"authToken", "authPassword", "authHeaderValue",
		"authValue", "authUsername", "authHeaders", "oauthConfig",
	} {
		if strings.Contains(got, field) {
			t.Fatalf("serialized gateway carries vendor field %q:\n%s", field, got)
		}
	}
}

// The shape of the auth configuration is still reported: an operator must be
// able to tell "no auth configured" from "auth I am not allowed to see".
func TestGatewayDTOReportsAuthShape(t *testing.T) {
	dto := GatewayFromSDK(fullyPopulatedGateway())
	if dto.AuthType != "bearer" {
		t.Fatalf("AuthType = %q, want bearer", dto.AuthType)
	}
	if !dto.AuthConfigured {
		t.Fatal("AuthConfigured = false, want true for a gateway carrying a token")
	}

	bare := &cf.Gateway{Name: "open", URL: "http://example/mcp"}
	if GatewayFromSDK(bare).AuthConfigured {
		t.Fatal("AuthConfigured = true for a gateway with no credentials")
	}
}

func TestGatewayDTOCarriesOperationalFields(t *testing.T) {
	dto := GatewayFromSDK(fullyPopulatedGateway())
	if dto.ID != "gw-1" || dto.Name != "mcp-svc-a" {
		t.Fatalf("identity = %+v", dto)
	}
	if dto.URL != "http://svc-a-mcp:8000/mcp" || dto.Transport != "streamablehttp" {
		t.Fatalf("routing = %+v", dto)
	}
	if !dto.Enabled || !dto.Reachable {
		t.Fatalf("state = %+v", dto)
	}
}

func TestVirtualServerDTODoesNotSerializeOAuthConfig(t *testing.T) {
	server := &cf.Server{
		ID:           "srv-1",
		Name:         "svc-a-catalog",
		Enabled:      true,
		OAuthEnabled: true,
		OAuthConfig:  map[string]any{"client_secret": sentinelOAuth},
	}
	encoded, err := json.Marshal(VirtualServerFromSDK(server))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), sentinelOAuth) {
		t.Fatalf("serialized virtual server leaked the oauth secret:\n%s", encoded)
	}
	if !VirtualServerFromSDK(server).OAuthEnabled {
		t.Fatal("OAuthEnabled = false, want the flag preserved")
	}
}

func TestNilInputsMapToZeroValues(t *testing.T) {
	if GatewayFromSDK(nil).Name != "" || VirtualServerFromSDK(nil).ID != "" || ToolFromSDK(nil).Name != "" {
		t.Fatal("nil SDK values should map to zero DTOs")
	}
	if len(GatewaysFromSDK(nil)) != 0 || len(ToolsFromSDK(nil)) != 0 {
		t.Fatal("nil slices should map to empty, non-nil slices")
	}
}
