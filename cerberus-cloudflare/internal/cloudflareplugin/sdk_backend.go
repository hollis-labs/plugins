package cloudflareplugin

import (
	"context"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/dns"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/zones"
)

// sdkBackend is the only code in this plugin that touches cloudflare-go. Every
// value leaving it has been mapped onto a DTO by an explicit function below;
// those functions are the security boundary and are meant to read like one.
type sdkBackend struct {
	client *cf.Client
}

// NewSDKBackend builds the live backend. The token is passed in rather than
// read here: the host resolves it and hands it over at init.
func NewSDKBackend(apiToken string) Backend {
	return &sdkBackend{client: cf.NewClient(option.WithAPIToken(apiToken))}
}

func (b *sdkBackend) ListZones(ctx context.Context) ([]Zone, error) {
	pager := b.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{})
	var out []Zone
	for pager.Next() {
		out = append(out, zoneFromSDK(pager.Current()))
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	return out, nil
}

func (b *sdkBackend) CreateZone(ctx context.Context, accountID, name, zoneType string) (*Zone, error) {
	params := zones.ZoneNewParams{
		Account: cf.F(zones.ZoneNewParamsAccount{ID: cf.F(accountID)}),
		Name:    cf.F(name),
	}
	if zoneType != "" {
		params.Type = cf.F(zones.Type(zoneType))
	}
	z, err := b.client.Zones.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("create zone %s in account %s: %w", name, accountID, err)
	}
	zone := zoneFromSDK(*z)
	return &zone, nil
}

func (b *sdkBackend) ListDNSRecords(ctx context.Context, zoneID string) ([]DNSRecord, error) {
	pager := b.client.DNS.Records.ListAutoPaging(ctx, dns.RecordListParams{ZoneID: cf.F(zoneID)})
	var out []DNSRecord
	for pager.Next() {
		out = append(out, recordFromSDK(pager.Current()))
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("list dns records for zone %s: %w", zoneID, err)
	}
	return out, nil
}

func (b *sdkBackend) CreateDNSRecord(ctx context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error) {
	body := dns.RecordNewParamsBody{
		Name:    cf.F(rec.Name),
		Type:    cf.F(dns.RecordNewParamsBodyType(rec.Type)),
		Content: cf.F(rec.Content),
		TTL:     cf.F(dns.TTL(rec.TTL)),
		Proxied: cf.F(rec.Proxied),
	}
	if rec.Priority != nil {
		body.Priority = cf.F(float64(*rec.Priority))
	}
	resp, err := b.client.DNS.Records.New(ctx, dns.RecordNewParams{ZoneID: cf.F(zoneID), Body: body})
	if err != nil {
		return nil, fmt.Errorf("create dns record in zone %s: %w", zoneID, err)
	}
	created := recordFromSDK(*resp)
	return &created, nil
}

func (b *sdkBackend) DeleteDNSRecord(ctx context.Context, zoneID, recordID string) error {
	if _, err := b.client.DNS.Records.Delete(ctx, recordID, dns.RecordDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
		return fmt.Errorf("delete dns record %s in zone %s: %w", recordID, zoneID, err)
	}
	return nil
}

// zoneFromSDK names every field that leaves. The SDK zone also carries the
// owning account, the owner's name and zone metadata; none of them is copied.
func zoneFromSDK(z zones.Zone) Zone {
	return Zone{
		ID:          z.ID,
		Name:        z.Name,
		Status:      string(z.Status),
		Paused:      z.Paused,
		NameServers: z.NameServers,
		Plan:        z.Plan.Name, //nolint:staticcheck // the field works; its deprecation is SDK-internal
	}
}

// recordFromSDK names every field that leaves. Comments, tags, metadata and
// the typed record data are not copied.
func recordFromSDK(r dns.RecordResponse) DNSRecord {
	rec := DNSRecord{
		ID:      r.ID,
		Type:    string(r.Type),
		Name:    r.Name,
		Content: r.Content,
		TTL:     int(r.TTL),
		Proxied: r.Proxied,
	}
	if r.Priority != 0 {
		p := int(r.Priority)
		rec.Priority = &p
	}
	return rec
}
