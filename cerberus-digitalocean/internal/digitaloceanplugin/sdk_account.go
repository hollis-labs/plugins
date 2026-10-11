package digitaloceanplugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/digitalocean/godo"
)

// walk pages a godo list call to its end under the same cap as list_droplets.
// fetch returns how many items it appended and the response.
func walk(fetch func(opt *godo.ListOptions) (int, *godo.Response, error)) (truncated bool, err error) {
	opt := &godo.ListOptions{Page: 1, PerPage: listPageSize}
	for {
		n, resp, err := fetch(opt)
		if err != nil {
			return false, err
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || n == 0 {
			return false, nil
		}
		if opt.Page >= maxListPages {
			return true, nil
		}
		opt.Page++
	}
}

func (b *sdkBackend) ListSSHKeys(ctx context.Context) (SSHKeyList, error) {
	out := SSHKeyList{Keys: []SSHKey{}}
	var err error
	out.Truncated, err = walk(func(opt *godo.ListOptions) (int, *godo.Response, error) {
		keys, resp, err := b.client.Keys.List(ctx, opt)
		for _, k := range keys {
			out.Keys = append(out.Keys, SSHKey{ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint})
		}
		return len(keys), resp, err
	})
	if err != nil {
		return SSHKeyList{}, fmt.Errorf("list ssh keys: %w", err)
	}
	return out, nil
}

func (b *sdkBackend) AddSSHKey(ctx context.Context, name, publicKey string) (*SSHKey, error) {
	key, _, err := b.client.Keys.Create(ctx, &godo.KeyCreateRequest{Name: name, PublicKey: publicKey})
	if err != nil {
		return nil, fmt.Errorf("add ssh key %s: %w", name, err)
	}
	if key == nil {
		return nil, fmt.Errorf("add ssh key %s: DigitalOcean returned no key", name)
	}
	return &SSHKey{ID: key.ID, Name: key.Name, Fingerprint: key.Fingerprint}, nil
}

func (b *sdkBackend) DeleteSSHKey(ctx context.Context, key string) error {
	var err error
	if id, convErr := strconv.Atoi(key); convErr == nil {
		_, err = b.client.Keys.DeleteByID(ctx, id)
	} else {
		_, err = b.client.Keys.DeleteByFingerprint(ctx, key)
	}
	if err != nil {
		return fmt.Errorf("delete ssh key %s: %w", key, err)
	}
	return nil
}

func (b *sdkBackend) TagResources(ctx context.Context, tag, resourceType string, ids []string) error {
	// A tag has to exist before it can be applied. Read first and create only
	// on a 404, so an existing tag is never re-created.
	if _, _, err := b.client.Tags.Get(ctx, tag); err != nil {
		var apiErr *godo.ErrorResponse
		if !errors.As(err, &apiErr) || apiErr.Response == nil || apiErr.Response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("read tag %s: %w", tag, err)
		}
		if _, _, err := b.client.Tags.Create(ctx, &godo.TagCreateRequest{Name: tag}); err != nil {
			return fmt.Errorf("create tag %s: %w", tag, err)
		}
	}
	req := &godo.TagResourcesRequest{}
	for _, id := range ids {
		req.Resources = append(req.Resources, godo.Resource{ID: id, Type: godo.ResourceType(resourceType)})
	}
	if _, err := b.client.Tags.TagResources(ctx, tag, req); err != nil {
		return fmt.Errorf("tag resources with %s: %w", tag, err)
	}
	return nil
}

func (b *sdkBackend) ListProjects(ctx context.Context) (ProjectList, error) {
	out := ProjectList{Projects: []Project{}}
	var err error
	out.Truncated, err = walk(func(opt *godo.ListOptions) (int, *godo.Response, error) {
		projects, resp, err := b.client.Projects.List(ctx, opt)
		for _, p := range projects {
			out.Projects = append(out.Projects, Project{ID: p.ID, Name: p.Name, Purpose: p.Purpose, Environment: p.Environment, IsDefault: p.IsDefault})
		}
		return len(projects), resp, err
	})
	if err != nil {
		return ProjectList{}, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

func (b *sdkBackend) AssignProjectResources(ctx context.Context, projectID string, urns []string) error {
	resources := make([]any, len(urns))
	for i, urn := range urns {
		resources[i] = urn
	}
	if _, _, err := b.client.Projects.AssignResources(ctx, projectID, resources...); err != nil {
		return fmt.Errorf("assign resources to project %s: %w", projectID, err)
	}
	return nil
}

func (b *sdkBackend) ListReservedIPs(ctx context.Context) (ReservedIPList, error) {
	out := ReservedIPList{ReservedIPs: []ReservedIP{}}
	var err error
	out.Truncated, err = walk(func(opt *godo.ListOptions) (int, *godo.Response, error) {
		ips, resp, err := b.client.ReservedIPs.List(ctx, opt)
		for _, ip := range ips {
			out.ReservedIPs = append(out.ReservedIPs, reservedIPFromSDK(&ip))
		}
		return len(ips), resp, err
	})
	if err != nil {
		return ReservedIPList{}, fmt.Errorf("list reserved ips: %w", err)
	}
	return out, nil
}

func (b *sdkBackend) AssignReservedIP(ctx context.Context, ip string, dropletID int) error {
	if _, _, err := b.client.ReservedIPActions.Assign(ctx, ip, dropletID); err != nil {
		return fmt.Errorf("assign reserved ip %s to droplet %d: %w", ip, dropletID, err)
	}
	return nil
}

func (b *sdkBackend) UnassignReservedIP(ctx context.Context, ip string) error {
	if _, _, err := b.client.ReservedIPActions.Unassign(ctx, ip); err != nil {
		return fmt.Errorf("unassign reserved ip %s: %w", ip, err)
	}
	return nil
}

func (b *sdkBackend) ListVPCs(ctx context.Context) (VPCList, error) {
	out := VPCList{VPCs: []VPC{}}
	var err error
	out.Truncated, err = walk(func(opt *godo.ListOptions) (int, *godo.Response, error) {
		vpcs, resp, err := b.client.VPCs.List(ctx, opt)
		for _, v := range vpcs {
			if v != nil {
				out.VPCs = append(out.VPCs, vpcFromSDK(v))
			}
		}
		return len(vpcs), resp, err
	})
	if err != nil {
		return VPCList{}, fmt.Errorf("list vpcs: %w", err)
	}
	return out, nil
}

func (b *sdkBackend) GetVPC(ctx context.Context, id string) (*VPC, error) {
	vpc, _, err := b.client.VPCs.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get vpc %s: %w", id, err)
	}
	out := vpcFromSDK(vpc)
	return &out, nil
}

// reservedIPFromSDK drops the embedded droplet (a full droplet record) down
// to its id, and the project id.
func reservedIPFromSDK(ip *godo.ReservedIP) ReservedIP {
	out := ReservedIP{IP: ip.IP, Locked: ip.Locked}
	if ip.Region != nil {
		out.Region = ip.Region.Slug
	}
	if ip.Droplet != nil {
		out.DropletID = ip.Droplet.ID
	}
	return out
}

func vpcFromSDK(v *godo.VPC) VPC {
	return VPC{ID: v.ID, Name: v.Name, Description: v.Description, Region: v.RegionSlug, IPRange: v.IPRange, Default: v.Default, CreatedAt: v.CreatedAt.UTC()}
}
