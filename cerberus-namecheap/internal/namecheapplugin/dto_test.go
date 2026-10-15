package namecheapplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// getList carries the account's username on every domain, and more attributes
// the compiled-in connector never returned. The DTO is an allow-list, so none
// of them reaches the caller.
const getListXML = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <CommandResponse Type="namecheap.domains.getList">
    <DomainGetListResult>
      <Domain ID="7" Name="example.com" User="SENTINEL-ACCOUNT-USER" Created="01/01/2020" Expires="01/01/2030" IsExpired="false" IsLocked="true" AutoRenew="true" WhoisGuard="ENABLED" IsPremium="false" IsOurDNS="true" />
    </DomainGetListResult>
  </CommandResponse>
</ApiResponse>`

func TestDomainDTOIsAnAllowList(t *testing.T) {
	domains, err := testClient(t, getListXML, nil).ListDomains(context.Background())
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	encoded, _ := json.Marshal(domains)
	got := string(encoded)
	for _, forbidden := range []string{"SENTINEL-ACCOUNT-USER", "IsPremium", "is_premium", "Created", "IsOurDNS"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("domain DTO emitted %q: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, `"name":"example.com"`) || !strings.Contains(got, `"auto_renew":true`) || !strings.Contains(got, `"is_locked":true`) {
		t.Fatalf("domain DTO lost its fields: %s", got)
	}
}
