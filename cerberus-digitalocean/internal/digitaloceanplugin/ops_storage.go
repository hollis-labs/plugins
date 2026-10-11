package digitaloceanplugin

import (
	"context"
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const maxVolumeGB = 16384

func init() {
	volTarget := []string{"volume_id"}
	volumeInput := func(extra map[string]any, required ...string) map[string]any {
		props := map[string]any{"volume_id": strProp("Volume id.")}
		for k, v := range extra {
			props[k] = v
		}
		return contract.ObjectSchema(props, append([]string{"volume_id"}, required...)...)
	}
	const volExample = "--arg volume_id=11111111-1111-4111-8111-111111111111"

	registerFleet(readOp("list_volumes", "digitalocean.account", nil, "List block storage volumes, optionally filtered by region or name.",
		contract.OutputSchemaOf[VolumeList](), contract.ObjectSchema(map[string]any{
			"region": strProp("Only volumes in this region slug."),
			"name":   strProp("Only the volume with this exact name."),
		}), "list_volumes"),
		false, func(p *Plugin, ctx context.Context, a *argMap, _ bool) (any, error) {
			region, name := a.str("region"), a.str("name")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListVolumes(ctx, region, name)
		})

	registerFleet(writeOp("create_volume", contract.EffectWrite, contract.CostBillable, "digitalocean.account", nil,
		"Create a block storage volume. BILLABLE: charged per GB-month from creation until the volume is deleted, attached or not.",
		contract.OutputSchemaOf[Volume](), contract.ObjectSchema(map[string]any{
			"name":           strProp("Volume name: lowercase letters, digits, hyphens."),
			"region":         strProp("Region slug."),
			"size_gigabytes": intProp("Size in GB."),
			"description":    strProp("Optional description."),
			"snapshot_id":    strProp("Optional volume snapshot to create the volume from."),
		}, "name", "region", "size_gigabytes"), "--arg name=data-1 --arg region=nyc3 --arg size_gigabytes=10"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			spec := VolumeSpec{Name: a.volumeName("name"), Region: a.required("region"), Description: a.str("description"), SnapshotID: a.optID("snapshot_id")}
			if size := a.positiveInt("size_gigabytes"); size > maxVolumeGB {
				a.problems = append(a.problems, fmt.Sprintf("size_gigabytes must be at most %d", maxVolumeGB))
			} else {
				spec.SizeGigabytes = int64(size)
			}
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("create_volume",
					fmt.Sprintf("Would create a %d GB block storage volume. BILLABLE: charged per GB-month until the volume is deleted.", spec.SizeGigabytes),
					map[string]any{"name": spec.Name, "region": spec.Region},
					map[string]any{"size_gigabytes": spec.SizeGigabytes, "description": spec.Description, "snapshot_id": spec.SnapshotID}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.CreateVolume(ctx, spec)
		})

	registerFleet(writeOp("attach_volume", contract.EffectWrite, contract.CostNone, "digitalocean.volume", volTarget,
		"Attach a volume to a droplet in the same region.",
		contract.OutputSchemaOf[ChangeResult](), volumeInput(map[string]any{"droplet_id": intProp("Droplet id.")}, "droplet_id"),
		volExample+" --arg droplet_id=123456"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			return volumeDroplet(p, ctx, a, dry, "attach_volume", "attach", "Would attach volume %s to droplet %d. They must be in the same region.",
				func(id string, d int) error { return p.backend.AttachVolume(ctx, id, d) })
		})

	registerFleet(writeOp("detach_volume", contract.EffectLifecycle, contract.CostNone, "digitalocean.volume", volTarget,
		"Detach a volume from a droplet. Unmount it on the droplet first: detaching a mounted volume can corrupt its filesystem. The volume and its data are kept and still bill.",
		contract.OutputSchemaOf[ChangeResult](), volumeInput(map[string]any{"droplet_id": intProp("Droplet id.")}, "droplet_id"),
		volExample+" --arg droplet_id=123456"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			return volumeDroplet(p, ctx, a, dry, "detach_volume", "detach",
				"Would detach volume %s from droplet %d. Unmount it on the droplet first; detaching a mounted volume can corrupt data. The volume is kept and still bills.",
				func(id string, d int) error { return p.backend.DetachVolume(ctx, id, d) })
		})

	registerFleet(writeOp("resize_volume", contract.EffectWrite, contract.CostBillable, "digitalocean.volume", volTarget,
		"Grow a volume. BILLABLE: the larger size is charged from now on. Volumes can only grow, never shrink; the filesystem must be extended on the droplet afterwards.",
		contract.OutputSchemaOf[ChangeResult](), volumeInput(map[string]any{
			"size_gigabytes": intProp("New size in GB; must exceed the current size."),
			"region":         strProp("The volume's region slug."),
		}, "size_gigabytes", "region"), volExample+" --arg size_gigabytes=20 --arg region=nyc3"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id, region := a.id("volume_id"), a.required("region")
			size := a.positiveInt("size_gigabytes")
			if size > maxVolumeGB {
				a.problems = append(a.problems, fmt.Sprintf("size_gigabytes must be at most %d", maxVolumeGB))
			}
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("resize_volume",
					fmt.Sprintf("Would grow volume %s to %d GB. BILLABLE: the larger size is charged from now on, and a volume can never be shrunk again.", id, size),
					map[string]any{"volume_id": id}, map[string]any{"size_gigabytes": size, "region": region}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.ResizeVolume(ctx, id, size, region); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "volume", ID: id, Action: "resize", Detail: []string{fmt.Sprintf("%d GB", size)}}, nil
		})

	registerFleet(writeOp("delete_volume", contract.EffectDestructive, contract.CostNone, "digitalocean.volume", volTarget,
		"Delete a volume permanently. Every file on it is lost; only a snapshot taken earlier can bring the data back. DigitalOcean refuses while it is attached.",
		contract.OutputSchemaOf[ChangeResult](), volumeInput(nil), volExample),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id := a.id("volume_id")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("delete_volume",
					fmt.Sprintf("Would permanently delete volume %s and ALL data on it. This cannot be undone; only an existing snapshot could restore it.", id),
					map[string]any{"volume_id": id}, nil).withWarnings(
					"Data loss: everything stored on the volume is destroyed. Snapshots of it are separate and are not deleted."), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.DeleteVolume(ctx, id); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "volume", ID: id, Action: "delete"}, nil
		})

	registerFleet(readOp("list_snapshots", "digitalocean.account", nil, "List volume or droplet snapshots.",
		contract.OutputSchemaOf[SnapshotList](), contract.ObjectSchema(map[string]any{
			"resource_type": strProp("volume or droplet."),
		}, "resource_type"), "list_snapshots --arg resource_type=volume"),
		false, func(p *Plugin, ctx context.Context, a *argMap, _ bool) (any, error) {
			kind := a.oneOf("resource_type", "volume", "droplet")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.ListSnapshots(ctx, kind)
		})

	registerFleet(writeOp("create_volume_snapshot", contract.EffectWrite, contract.CostBillable, "digitalocean.volume", volTarget,
		"Snapshot a volume. BILLABLE: snapshot storage is charged per GB-month until the snapshot is deleted.",
		contract.OutputSchemaOf[Snapshot](), volumeInput(map[string]any{"name": strProp("Snapshot name.")}, "name"),
		volExample+" --arg name=data-1-before-upgrade"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id, name := a.id("volume_id"), a.label("name", 255)
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("create_volume_snapshot",
					fmt.Sprintf("Would snapshot volume %s. BILLABLE: snapshot storage is charged per GB-month until the snapshot is deleted.", id),
					map[string]any{"volume_id": id}, map[string]any{"name": name}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			return p.backend.CreateVolumeSnapshot(ctx, id, name)
		})

	registerFleet(writeOp("create_droplet_snapshot", contract.EffectWrite, contract.CostBillable, "digitalocean.droplet", []string{"droplet_id"},
		"Snapshot a droplet's disk. BILLABLE: snapshot storage is charged per GB-month until the snapshot is deleted. Power the droplet off first for a consistent image; the snapshot completes asynchronously.",
		contract.OutputSchemaOf[SnapshotStarted](), contract.ObjectSchema(map[string]any{
			"droplet_id": intProp("Droplet id."), "name": strProp("Snapshot name."),
		}, "droplet_id", "name"), "--arg droplet_id=123456 --arg name=web-1-before-upgrade"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id, name := a.dropletID(), a.label("name", 255)
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("create_droplet_snapshot",
					fmt.Sprintf("Would snapshot droplet %d. BILLABLE: snapshot storage is charged per GB-month until the snapshot is deleted. Power off first for a consistent image.", id),
					map[string]any{"droplet_id": id}, map[string]any{"name": name}), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.CreateDropletSnapshot(ctx, id, name); err != nil {
				return nil, err
			}
			return SnapshotStarted{DropletID: id, Name: name, Action: "snapshot"}, nil
		})

	registerFleet(writeOp("delete_snapshot", contract.EffectDestructive, contract.CostNone, "digitalocean.snapshot", []string{"snapshot_id"},
		"Delete a volume or droplet snapshot permanently. The point-in-time copy is lost and cannot be restored.",
		contract.OutputSchemaOf[ChangeResult](), contract.ObjectSchema(map[string]any{"snapshot_id": strProp("Snapshot id.")}, "snapshot_id"),
		"--arg snapshot_id=22222222-2222-4222-8222-222222222222"),
		true, func(p *Plugin, ctx context.Context, a *argMap, dry bool) (any, error) {
			id := a.id("snapshot_id")
			if err := invalid(a.err()); err != nil {
				return nil, err
			}
			if dry {
				return newPreview("delete_snapshot",
					fmt.Sprintf("Would permanently delete snapshot %s. The point-in-time copy is lost; if it is the only copy of that data it cannot be recovered.", id),
					map[string]any{"snapshot_id": id}, nil).withWarnings(
					"Data loss: this snapshot may be the only backup of a volume or droplet."), nil
			}
			if err := p.ready(); err != nil {
				return nil, err
			}
			if err := p.backend.DeleteSnapshot(ctx, id); err != nil {
				return nil, err
			}
			return ChangeResult{Resource: "snapshot", ID: id, Action: "delete"}, nil
		})
}

// volumeDroplet is attach and detach: a volume and a droplet, both named in the
// preview.
func volumeDroplet(p *Plugin, _ context.Context, a *argMap, dry bool, op, action, summary string, run func(volumeID string, dropletID int) error) (any, error) {
	volume, droplet := a.id("volume_id"), a.dropletID()
	if err := invalid(a.err()); err != nil {
		return nil, err
	}
	if dry {
		return newPreview(op, fmt.Sprintf(summary, volume, droplet),
			map[string]any{"volume_id": volume, "droplet_id": droplet}, nil), nil
	}
	if err := p.ready(); err != nil {
		return nil, err
	}
	if err := run(volume, droplet); err != nil {
		return nil, err
	}
	return ChangeResult{Resource: "volume", ID: volume, Action: action, DropletID: droplet}, nil
}
