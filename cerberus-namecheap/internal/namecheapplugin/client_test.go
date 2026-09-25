package namecheapplugin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A recorded getHosts response, the shape Namecheap returns, with both host
// element spellings the compiled-in connector accepted.
const getHostsXML = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="OK" xmlns="http://api.namecheap.com/xml.response">
  <CommandResponse Type="namecheap.domains.dns.getHosts">
    <DomainDNSGetHostsResult Domain="example.com" EmailType="MX" IsUsingOurDNS="true">
      <host HostId="11" Name="@" Type="A" Address="203.0.113.10" MXPref="10" TTL="300" AssociatedAppTitle="" FriendlyName="" IsActive="true" IsDDNSEnabled="false" />
      <Host HostId="12" Name="@" Type="MX" Address="mail.example.com." MXPref="10" TTL="1800" />
    </DomainDNSGetHostsResult>
  </CommandResponse>
</ApiResponse>`

const errorXML = `<?xml version="1.0" encoding="utf-8"?>
<ApiResponse Status="ERROR" xmlns="http://api.namecheap.com/xml.response">
  <Errors><Error Number="1011150">Invalid request IP: 198.51.100.7</Error></Errors>
</ApiResponse>`

func testClient(t *testing.T, body string, seen *string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.RawQuery
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c := NewClient("user", "key-value", "account", "198.51.100.7")
	c.baseURL = server.URL
	return c
}

func TestGetDNSRecordSetParsesBothHostSpellings(t *testing.T) {
	var query string
	set, err := testClient(t, getHostsXML, &query).GetDNSRecordSet(context.Background(), "example", "com")
	if err != nil {
		t.Fatalf("GetDNSRecordSet: %v", err)
	}
	if set.EmailType != "MX" || len(set.Records) != 2 || set.Records[0].ID != 11 || set.Records[1].TTL != 1800 || set.Records[1].MXPref != 10 {
		t.Fatalf("set = %+v", set)
	}
	for _, want := range []string{"Command=namecheap.domains.dns.getHosts", "SLD=example", "TLD=com", "ClientIp=198.51.100.7"} {
		if !strings.Contains(query, want) {
			t.Errorf("request query lacks %q: %s", want, query)
		}
	}
}

func TestAPIErrorsCarryNamecheapNumbers(t *testing.T) {
	_, err := testClient(t, errorXML, nil).ListDomains(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || len(apiErr.Numbers) != 1 || apiErr.Numbers[0] != "1011150" {
		t.Fatalf("err = %#v", err)
	}
	if !isAddressRefused(err) {
		t.Fatal("an allow-list refusal was not recognised")
	}
}

func TestSetDNSRecordSetSendsEveryRecord(t *testing.T) {
	var query string
	ok := `<ApiResponse Status="OK"><CommandResponse><DomainDNSSetHostsResult IsSuccess="true"/></CommandResponse></ApiResponse>`
	err := testClient(t, ok, &query).SetDNSRecordSet(context.Background(), "example", "com", DNSRecordSet{EmailType: "MX", Records: []DNSRecord{
		{Type: "A", Host: "@", Value: "203.0.113.10", TTL: 300},
		{Type: "MX", Host: "@", Value: "mail.example.com", MXPref: 10},
	}})
	if err != nil {
		t.Fatalf("SetDNSRecordSet: %v", err)
	}
	for _, want := range []string{"Command=namecheap.domains.dns.setHosts", "EmailType=MX", "HostName1=%40", "RecordType2=MX", "MXPref2=10", "TTL1=300"} {
		if !strings.Contains(query, want) {
			t.Errorf("setHosts query lacks %q: %s", want, query)
		}
	}
}
