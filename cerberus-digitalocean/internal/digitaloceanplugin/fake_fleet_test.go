package digitaloceanplugin

import (
	"context"
	"fmt"
)

// The fleet half of fakeBackend. Fakes only: no test reaches DigitalOcean, and
// every id, address and name below is an example value.

func (f *fakeBackend) ListFirewalls(context.Context) (FirewallList, error) {
	f.record("ListFirewalls")
	return FirewallList{Firewalls: append([]Firewall(nil), f.firewalls...)}, f.err
}

func (f *fakeBackend) findFirewall(id string) (*Firewall, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.firewalls) == 0 {
		return &Firewall{ID: id, Name: "fw"}, nil
	}
	for i := range f.firewalls {
		if f.firewalls[i].ID == id {
			fw := f.firewalls[i]
			return &fw, nil
		}
	}
	return nil, fmt.Errorf("firewall %s not found", id)
}

func (f *fakeBackend) GetFirewall(_ context.Context, id string) (*Firewall, error) {
	f.record("GetFirewall", id)
	return f.findFirewall(id)
}

func (f *fakeBackend) CreateFirewall(_ context.Context, spec FirewallSpec) (*Firewall, error) {
	f.record("CreateFirewall", spec)
	if f.err != nil {
		return nil, f.err
	}
	return &Firewall{ID: exampleFirewallID, Name: spec.Name, Status: "waiting", InboundRules: spec.InboundRules, OutboundRules: spec.OutboundRules, DropletIDs: spec.DropletIDs, Tags: spec.Tags}, nil
}

func (f *fakeBackend) UpdateFirewall(_ context.Context, id string, spec FirewallSpec) (*Firewall, error) {
	f.record("UpdateFirewall", id, spec)
	if f.err != nil {
		return nil, f.err
	}
	return &Firewall{ID: id, Name: spec.Name, InboundRules: spec.InboundRules, OutboundRules: spec.OutboundRules, DropletIDs: spec.DropletIDs, Tags: spec.Tags}, nil
}

func (f *fakeBackend) DeleteFirewall(_ context.Context, id string) error {
	f.record("DeleteFirewall", id)
	return f.err
}

func (f *fakeBackend) AddFirewallDroplets(_ context.Context, id string, ids []int) error {
	f.record("AddFirewallDroplets", id, ids)
	return f.err
}

func (f *fakeBackend) RemoveFirewallDroplets(_ context.Context, id string, ids []int) error {
	f.record("RemoveFirewallDroplets", id, ids)
	return f.err
}

const exampleFirewallID = "00000000-0000-4000-8000-000000000000"

// fleetValidArgs is a complete acknowledged argument set for each fleet
// operation, so the generic tests can iterate the manifest.
func fleetValidArgs(operation string) (map[string]any, bool) {
	ack := argAcknowledged
	switch operation {
	case "list_firewalls":
		return map[string]any{}, true
	case "get_firewall":
		return map[string]any{"firewall_id": exampleFirewallID}, true
	case "create_firewall":
		return map[string]any{"name": "tailnet-only", ack: true}, true
	case "update_firewall":
		return map[string]any{"firewall_id": exampleFirewallID, "name": "renamed", ack: true}, true
	case "delete_firewall":
		return map[string]any{"firewall_id": exampleFirewallID, ack: true}, true
	case "assign_firewall_droplets", "unassign_firewall_droplets":
		return map[string]any{"firewall_id": exampleFirewallID, "droplet_ids": []any{float64(42)}, ack: true}, true
	}
	return nil, false
}
