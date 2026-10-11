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

func (f *fakeBackend) ListVolumes(_ context.Context, region, name string) (VolumeList, error) {
	f.record("ListVolumes", region, name)
	return VolumeList{Volumes: append([]Volume(nil), f.volumes...)}, f.err
}

func (f *fakeBackend) CreateVolume(_ context.Context, req VolumeSpec) (*Volume, error) {
	f.record("CreateVolume", req)
	if f.err != nil {
		return nil, f.err
	}
	return &Volume{ID: exampleVolumeID, Name: req.Name, Region: req.Region, SizeGigabytes: req.SizeGigabytes}, nil
}

func (f *fakeBackend) AttachVolume(_ context.Context, id string, droplet int) error {
	f.record("AttachVolume", id, droplet)
	return f.err
}

func (f *fakeBackend) DetachVolume(_ context.Context, id string, droplet int) error {
	f.record("DetachVolume", id, droplet)
	return f.err
}

func (f *fakeBackend) ResizeVolume(_ context.Context, id string, size int, region string) error {
	f.record("ResizeVolume", id, size, region)
	return f.err
}

func (f *fakeBackend) DeleteVolume(_ context.Context, id string) error {
	f.record("DeleteVolume", id)
	return f.err
}

func (f *fakeBackend) ListSnapshots(_ context.Context, kind string) (SnapshotList, error) {
	f.record("ListSnapshots", kind)
	return SnapshotList{Snapshots: append([]Snapshot(nil), f.snapshots...)}, f.err
}

func (f *fakeBackend) CreateVolumeSnapshot(_ context.Context, id, name string) (*Snapshot, error) {
	f.record("CreateVolumeSnapshot", id, name)
	if f.err != nil {
		return nil, f.err
	}
	return &Snapshot{ID: exampleSnapshotID, Name: name, ResourceID: id, ResourceType: "volume"}, nil
}

func (f *fakeBackend) CreateDropletSnapshot(_ context.Context, id int, name string) error {
	f.record("CreateDropletSnapshot", id, name)
	return f.err
}

func (f *fakeBackend) DeleteSnapshot(_ context.Context, id string) error {
	f.record("DeleteSnapshot", id)
	return f.err
}

const (
	exampleVolumeID   = "11111111-1111-4111-8111-111111111111"
	exampleSnapshotID = "22222222-2222-4222-8222-222222222222"
)

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
	case "list_volumes":
		return map[string]any{}, true
	case "create_volume":
		return map[string]any{"name": "data-1", "region": "nyc3", "size_gigabytes": float64(10), ack: true}, true
	case "attach_volume", "detach_volume":
		return map[string]any{"volume_id": exampleVolumeID, "droplet_id": float64(42), ack: true}, true
	case "resize_volume":
		return map[string]any{"volume_id": exampleVolumeID, "size_gigabytes": float64(20), "region": "nyc3", ack: true}, true
	case "delete_volume":
		return map[string]any{"volume_id": exampleVolumeID, ack: true}, true
	case "list_snapshots":
		return map[string]any{"resource_type": "volume"}, true
	case "create_volume_snapshot":
		return map[string]any{"volume_id": exampleVolumeID, "name": "before-upgrade", ack: true}, true
	case "create_droplet_snapshot":
		return map[string]any{"droplet_id": float64(42), "name": "before-upgrade", ack: true}, true
	case "delete_snapshot":
		return map[string]any{"snapshot_id": exampleSnapshotID, ack: true}, true
	}
	return nil, false
}
