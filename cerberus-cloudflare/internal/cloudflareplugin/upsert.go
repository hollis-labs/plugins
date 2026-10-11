package cloudflareplugin

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// tailscaleRange is the CGNAT block Tailscale assigns node addresses from.
var tailscaleRange = netip.MustParsePrefix("100.64.0.0/10")

// requireTailscaleAddress refuses anything but an IPv4 address inside
// 100.64.0.0/10. upsert_a_record exists to publish host records that resolve to
// a tailnet address, so it must be impossible to point one at a public address
// by accident, whether by typo, a copied value or a prompt that was steered.
func requireTailscaleAddress(content string) error {
	addr, err := netip.ParseAddr(content)
	if err != nil || !addr.Is4() {
		return fmt.Errorf("content must be an IPv4 Tailscale address in 100.64.0.0/10; %q is not an IPv4 address", content)
	}
	if !tailscaleRange.Contains(addr) {
		return fmt.Errorf("content must be an IPv4 Tailscale address in 100.64.0.0/10; %s is outside that range, and upsert_a_record will not publish any other address", addr)
	}
	return nil
}

var recordNamePattern = regexp.MustCompile(`^(@|[A-Za-z0-9_]([A-Za-z0-9_.-]*[A-Za-z0-9_])?)$`)

// qualifiedName turns the name a caller gave into the fully qualified, lower
// case name Cloudflare stores: the zone apex for @, a name already inside the
// zone, or a relative name placed under it. The preview shows the result, so a
// name meant for another zone is visible before it is acknowledged.
func qualifiedName(name, zone string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	zone = strings.ToLower(zone)
	switch {
	case name == "@" || name == zone:
		return zone
	case strings.HasSuffix(name, "."+zone):
		return name
	}
	return name + "." + zone
}

// upsertPlan is what upsert_a_record would do, worked out from Cloudflare's
// current state. Preview and the real call both build it, so the preview is the
// call's own plan and not a separate claim.
type upsertPlan struct {
	Action string // created, updated or unchanged
	Before *DNSRecord
	After  DNSRecord
}

// planUpsert reads the zone and the existing A records at the name. Exactly
// zero records means create, one means update (or nothing, if it already
// matches), more than one is ambiguous and refused.
func (p *Plugin) planUpsert(ctx context.Context, zoneID, name, content string, ttl *int) (upsertPlan, error) {
	zone, err := p.backend.GetZone(ctx, zoneID)
	if err != nil {
		return upsertPlan{}, err
	}
	fqdn := qualifiedName(name, zone.Name)
	existing, err := p.backend.FindARecords(ctx, zoneID, fqdn)
	if err != nil {
		return upsertPlan{}, err
	}
	plan := upsertPlan{After: DNSRecord{Type: "A", Name: fqdn, Content: content, TTL: 1, Proxied: false}}
	if ttl != nil {
		plan.After.TTL = *ttl
	}
	switch len(existing) {
	case 0:
		plan.Action = "created"
	case 1:
		before := existing[0]
		plan.Before = &before
		plan.After.ID = before.ID
		if ttl == nil {
			plan.After.TTL = before.TTL
		}
		plan.Action = "updated"
		if before.Content == plan.After.Content && before.TTL == plan.After.TTL && !before.Proxied {
			plan.Action = "unchanged"
		}
	default:
		return upsertPlan{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%d A records already exist at %s; upsert_a_record will not choose between them. Delete the extras with delete_dns_record first", len(existing), fqdn))
	}
	return plan, nil
}

func (p *Plugin) upsertPreview(ctx context.Context, zoneID, name, content string, ttl *int) (DryRunPreview, error) {
	if p.backend == nil {
		return newPreview("upsert_a_record", "Would create or update a DNS-only A record. Nothing is changed by this preview.",
			map[string]any{"zone_id": zoneID, "name": name, "type": "A"},
			map[string]any{"after": map[string]any{"content": content, "ttl": ttl, "proxied": false}},
			"the current record was not read: no Cloudflare API token reached this plugin, so there is no before to compare and this preview is the request alone"), nil
	}
	plan, err := p.planUpsert(ctx, zoneID, name, content, ttl)
	if err != nil {
		return DryRunPreview{}, err
	}
	var warnings []string
	switch plan.Action {
	case "unchanged":
		warnings = append(warnings, "the record already matches, so the real call would change nothing")
	case "updated":
		if plan.Before.Proxied {
			warnings = append(warnings, "the record is currently proxied through Cloudflare; this turns the proxy off")
		}
		warnings = append(warnings, "the name resolves to the new address at once, for anything that has not cached the old one")
	}
	return newPreview("upsert_a_record", "Would "+previewVerb(plan.Action)+" a DNS-only A record. Nothing is changed by this preview.",
		map[string]any{"zone_id": zoneID, "name": plan.After.Name, "type": "A"},
		map[string]any{"action": plan.Action, "before": plan.Before, "after": plan.After}, warnings...), nil
}

func previewVerb(action string) string {
	switch action {
	case "created":
		return "create"
	case "updated":
		return "update"
	}
	return "leave unchanged"
}

func (p *Plugin) upsert(ctx context.Context, zoneID, name, content string, ttl *int) (UpsertResult, error) {
	plan, err := p.planUpsert(ctx, zoneID, name, content, ttl)
	if err != nil {
		return UpsertResult{}, err
	}
	switch plan.Action {
	case "unchanged":
		return UpsertResult{Action: plan.Action, Record: *plan.Before}, nil
	case "updated":
		updated, err := p.backend.UpdateDNSRecord(ctx, zoneID, plan.Before.ID, plan.After)
		if err != nil {
			return UpsertResult{}, err
		}
		return UpsertResult{Action: plan.Action, Record: *updated, Previous: plan.Before}, nil
	}
	created, err := p.backend.CreateDNSRecord(ctx, zoneID, plan.After)
	if err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{Action: plan.Action, Record: *created}, nil
}
