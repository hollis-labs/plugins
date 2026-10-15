package cloudflareplugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/zones"
)

// Sentinels are distinctive so a leak is unambiguous in the serialized output
// rather than a substring of something innocent.
const (
	sentinelAccountID   = "SENTINEL-ACCOUNT-ID-4d8e"
	sentinelAccountName = "SENTINEL-ACCOUNT-NAME-1a6f"
	sentinelOwnerName   = "SENTINEL-OWNER-NAME-7c5b"
	sentinelOwnerID     = "SENTINEL-OWNER-ID-2e9d"
	sentinelComment     = "SENTINEL-RECORD-COMMENT-8b4a"
	sentinelTag         = "SENTINEL-RECORD-TAG-3c1f"
	sentinelOriginalNS  = "sentinel-original-ns.example"
)

// The DTO is an allow-list (ADR 0003). A zone carries who owns it; that must
// not reach CLI output, MCP results or an agent's context.
func TestZoneDTOOmitsEverythingNotNamed(t *testing.T) {
	sdk := zones.Zone{
		ID:                  "zone-1",
		Name:                "example.com",
		Status:              zones.ZoneStatusActive,
		NameServers:         []string{"a.ns.example"},
		OriginalNameServers: []string{sentinelOriginalNS},
		Account:             zones.ZoneAccount{ID: sentinelAccountID, Name: sentinelAccountName},
		Owner:               zones.ZoneOwner{ID: sentinelOwnerID, Name: sentinelOwnerName, Type: "user"},
		Plan:                zones.ZonePlan{Name: "Free Website"}, //nolint:staticcheck // mirrors the mapped field
	}
	encoded, err := json.Marshal(zoneFromSDK(sdk))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)
	for _, secret := range []string{sentinelAccountID, sentinelAccountName, sentinelOwnerID, sentinelOwnerName, sentinelOriginalNS} {
		if strings.Contains(got, secret) {
			t.Fatalf("serialized zone leaked %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{`"id":"zone-1"`, `"status":"active"`, `"plan":"Free Website"`, `"a.ns.example"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("serialized zone lost %s:\n%s", want, got)
		}
	}
}

func TestDNSRecordDTOOmitsEverythingNotNamed(t *testing.T) {
	sdk := dns.RecordResponse{
		ID:       "rec-1",
		Type:     dns.RecordResponseTypeMX,
		Name:     "example.com",
		Content:  "mail.example.com",
		TTL:      300,
		Proxied:  false,
		Priority: 10,
		Comment:  sentinelComment,
		Tags:     []dns.RecordTags{sentinelTag},
		Meta:     map[string]any{"note": sentinelComment},
	}
	encoded, err := json.Marshal(recordFromSDK(sdk))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)
	for _, secret := range []string{sentinelComment, sentinelTag} {
		if strings.Contains(got, secret) {
			t.Fatalf("serialized record leaked %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, `"priority":10`) || !strings.Contains(got, `"type":"MX"`) {
		t.Fatalf("serialized record lost a named field:\n%s", got)
	}
}

// Priority zero means "not an MX record" in the SDK; it must be omitted, not
// reported as priority 0.
func TestDNSRecordWithoutPriorityOmitsIt(t *testing.T) {
	encoded, err := json.Marshal(recordFromSDK(dns.RecordResponse{ID: "r", Type: dns.RecordResponseTypeA}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "priority") {
		t.Fatalf("priority serialized for a record without one: %s", encoded)
	}
}
