package cloudflareplugin

import "context"

// Backend is everything this plugin asks of Cloudflare. It returns this
// package's DTOs, never cloudflare-go types, so the SDK is confined to
// sdk_backend.go and a connector built on Backend structurally cannot return a
// vendor struct.
type Backend interface {
	ListZones(ctx context.Context) ([]Zone, error)
	CreateZone(ctx context.Context, accountID, name, zoneType string) (*Zone, error)
	ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)
	CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error)
	DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error
}
