package namecheapplugin

import (
	"context"
	"fmt"
	"strings"
)

// Backend is everything this plugin asks of Namecheap. It returns this
// package's DTOs, never the XML response types, which stay in client.go.
type Backend interface {
	ListDomains(ctx context.Context) ([]Domain, error)
	GetDomainStatus(ctx context.Context, domain string) (*DomainStatus, error)
	GetDNSRecordSet(ctx context.Context, sld, tld string) (*DNSRecordSet, error)
	SetDNSRecordSet(ctx context.Context, sld, tld string, set DNSRecordSet) error
	SetCustomNameservers(ctx context.Context, domain string, nameservers []string) (*DomainNameserverUpdate, error)
}

// SplitDomain splits a domain name into SLD and TLD at the first dot, as
// Namecheap's API wants it: "example.co.uk" is ("example", "co.uk").
func SplitDomain(domain string) (string, string, error) {
	parts := strings.SplitN(domain, ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid domain %q: expected format sld.tld", domain)
	}
	return parts[0], parts[1], nil
}
