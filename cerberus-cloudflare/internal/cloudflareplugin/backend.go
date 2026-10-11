package cloudflareplugin

import "context"

// Backend is everything this plugin asks of Cloudflare. It returns this
// package's DTOs, never cloudflare-go types or decoded vendor JSON, so the
// vendor shapes are confined to sdk_backend.go and api_backend.go and a
// connector built on Backend structurally cannot return a vendor struct.
type Backend interface {
	ListZones(ctx context.Context) ([]Zone, error)
	CreateZone(ctx context.Context, accountID, name, zoneType string) (*Zone, error)
	ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error)
	CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error)
	DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error

	// VerifyToken verifies the plugin's own token. With an account id it asks
	// the account-owned token endpoint, without one the user-owned one.
	VerifyToken(ctx context.Context, accountID string) (*TokenStatus, error)

	// R2 buckets. An empty jurisdiction is the default one.
	ListBuckets(ctx context.Context, accountID, jurisdiction string) ([]Bucket, error)
	CreateBucket(ctx context.Context, accountID string, req BucketRequest) (*Bucket, error)
	// DeleteBucket is refused by Cloudflare while the bucket holds objects.
	DeleteBucket(ctx context.Context, accountID, name, jurisdiction string) error
}

// BucketRequest is what create_r2_bucket sends. It is ours, not the wire body.
type BucketRequest struct {
	Name         string
	Jurisdiction string
	LocationHint string
	StorageClass string
}
