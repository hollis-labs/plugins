package digitaloceanplugin

import (
	"context"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
)

func (b *sdkBackend) ListVolumes(ctx context.Context, region, name string) (VolumeList, error) {
	out := VolumeList{Volumes: []Volume{}}
	params := &godo.ListVolumeParams{Region: region, Name: name, ListOptions: &godo.ListOptions{Page: 1, PerPage: listPageSize}}
	for {
		volumes, resp, err := b.client.Storage.ListVolumes(ctx, params)
		if err != nil {
			return VolumeList{}, fmt.Errorf("list volumes: %w", err)
		}
		for i := range volumes {
			out.Volumes = append(out.Volumes, volumeFromSDK(&volumes[i]))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(volumes) == 0 {
			return out, nil
		}
		if params.ListOptions.Page >= maxListPages {
			out.Truncated = true
			return out, nil
		}
		params.ListOptions.Page++
	}
}

func (b *sdkBackend) CreateVolume(ctx context.Context, req VolumeSpec) (*Volume, error) {
	vol, _, err := b.client.Storage.CreateVolume(ctx, &godo.VolumeCreateRequest{
		Name: req.Name, Region: req.Region, SizeGigaBytes: req.SizeGigabytes,
		Description: req.Description, SnapshotID: req.SnapshotID,
	})
	if err != nil {
		return nil, fmt.Errorf("create volume %s: %w", req.Name, err)
	}
	if vol == nil {
		return nil, fmt.Errorf("create volume %s: DigitalOcean returned no volume", req.Name)
	}
	out := volumeFromSDK(vol)
	return &out, nil
}

func (b *sdkBackend) AttachVolume(ctx context.Context, volumeID string, dropletID int) error {
	if _, _, err := b.client.StorageActions.Attach(ctx, volumeID, dropletID); err != nil {
		return fmt.Errorf("attach volume %s to droplet %d: %w", volumeID, dropletID, err)
	}
	return nil
}

func (b *sdkBackend) DetachVolume(ctx context.Context, volumeID string, dropletID int) error {
	if _, _, err := b.client.StorageActions.DetachByDropletID(ctx, volumeID, dropletID); err != nil {
		return fmt.Errorf("detach volume %s from droplet %d: %w", volumeID, dropletID, err)
	}
	return nil
}

func (b *sdkBackend) ResizeVolume(ctx context.Context, volumeID string, sizeGigabytes int, region string) error {
	if _, _, err := b.client.StorageActions.Resize(ctx, volumeID, sizeGigabytes, region); err != nil {
		return fmt.Errorf("resize volume %s: %w", volumeID, err)
	}
	return nil
}

func (b *sdkBackend) DeleteVolume(ctx context.Context, volumeID string) error {
	if _, err := b.client.Storage.DeleteVolume(ctx, volumeID); err != nil {
		return fmt.Errorf("delete volume %s: %w", volumeID, err)
	}
	return nil
}

func (b *sdkBackend) ListSnapshots(ctx context.Context, resourceType string) (SnapshotList, error) {
	list := b.client.Snapshots.ListVolume
	if resourceType == "droplet" {
		list = b.client.Snapshots.ListDroplet
	}
	out := SnapshotList{Snapshots: []Snapshot{}}
	opt := &godo.ListOptions{Page: 1, PerPage: listPageSize}
	for {
		snapshots, resp, err := list(ctx, opt)
		if err != nil {
			return SnapshotList{}, fmt.Errorf("list %s snapshots: %w", resourceType, err)
		}
		for i := range snapshots {
			out.Snapshots = append(out.Snapshots, snapshotFromSDK(&snapshots[i]))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(snapshots) == 0 {
			return out, nil
		}
		if opt.Page >= maxListPages {
			out.Truncated = true
			return out, nil
		}
		opt.Page++
	}
}

func (b *sdkBackend) CreateVolumeSnapshot(ctx context.Context, volumeID, name string) (*Snapshot, error) {
	snap, _, err := b.client.Storage.CreateSnapshot(ctx, &godo.SnapshotCreateRequest{VolumeID: volumeID, Name: name})
	if err != nil {
		return nil, fmt.Errorf("snapshot volume %s: %w", volumeID, err)
	}
	if snap == nil {
		return nil, fmt.Errorf("snapshot volume %s: DigitalOcean returned no snapshot", volumeID)
	}
	out := snapshotFromSDK(snap)
	return &out, nil
}

func (b *sdkBackend) CreateDropletSnapshot(ctx context.Context, dropletID int, name string) error {
	if _, _, err := b.client.DropletActions.Snapshot(ctx, dropletID, name); err != nil {
		return fmt.Errorf("snapshot droplet %d: %w", dropletID, err)
	}
	return nil
}

func (b *sdkBackend) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	if _, err := b.client.Snapshots.Delete(ctx, snapshotID); err != nil {
		return fmt.Errorf("delete snapshot %s: %w", snapshotID, err)
	}
	return nil
}

// volumeFromSDK names every field that leaves. The filesystem label is not
// copied.
func volumeFromSDK(v *godo.Volume) Volume {
	out := Volume{
		ID: v.ID, Name: v.Name, SizeGigabytes: v.SizeGigaBytes, Description: v.Description,
		DropletIDs: append([]int{}, v.DropletIDs...), FilesystemType: v.FilesystemType,
		CreatedAt: v.CreatedAt.UTC(), Tags: append([]string{}, v.Tags...),
	}
	if v.Region != nil {
		out.Region = v.Region.Slug
	}
	return out
}

func snapshotFromSDK(s *godo.Snapshot) Snapshot {
	created, _ := time.Parse(time.RFC3339, s.Created)
	return Snapshot{
		ID: s.ID, Name: s.Name, ResourceID: s.ResourceID, ResourceType: s.ResourceType,
		Regions: s.Regions, MinDiskSizeGB: s.MinDiskSize, SizeGigabytes: s.SizeGigaBytes,
		CreatedAt: created.UTC(), Tags: s.Tags,
	}
}
