package digitaloceanplugin

import (
	"context"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
)

// listPageSize is the page size list_droplets asks for. DigitalOcean caps
// per_page at 200.
const listPageSize = 200

// maxListPages bounds the walk: 100 pages of 200 is 20,000 droplets, and a
// server whose links never end must not hold the call open forever.
const maxListPages = 100

// sdkBackend is the only code in this plugin that touches godo. Every value
// leaving it has been mapped onto a DTO by an explicit function below; those
// functions are the security boundary and are meant to read like one.
type sdkBackend struct {
	client *godo.Client
}

// NewSDKBackend builds the live backend. The token is passed in rather than
// read here: the host resolves it and hands it over at init.
func NewSDKBackend(apiToken string) Backend {
	return &sdkBackend{client: godo.NewFromToken(apiToken)}
}

// newSDKBackendWithClient is the test seam for a godo client pointed at a
// local server.
func newSDKBackendWithClient(client *godo.Client) Backend {
	return &sdkBackend{client: client}
}

// ListDroplets reads every page. The compiled-in connector read one page of
// 100 and silently dropped the rest. At maxListPages it stops and returns
// what it has, marked Truncated rather than failing or pretending to be done.
func (b *sdkBackend) ListDroplets(ctx context.Context) (DropletList, error) {
	out := DropletList{Droplets: []DropletStatus{}}
	opt := &godo.ListOptions{Page: 1, PerPage: listPageSize}
	for {
		droplets, resp, err := b.client.Droplets.List(ctx, opt)
		if err != nil {
			return DropletList{}, fmt.Errorf("list droplets: %w", err)
		}
		for i := range droplets {
			out.Droplets = append(out.Droplets, dropletFromSDK(&droplets[i]))
		}
		// Stop on the last page, and on an empty one: a response that claims
		// more pages but returns nothing must not loop forever. The page
		// number is counted here rather than read back from the links, which
		// derive it from a "prev" link DigitalOcean may omit.
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(droplets) == 0 {
			return out, nil
		}
		if opt.Page >= maxListPages {
			out.Truncated = true
			return out, nil
		}
		opt.Page++
	}
}

func (b *sdkBackend) GetDroplet(ctx context.Context, id int) (*DropletStatus, error) {
	droplet, _, err := b.client.Droplets.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get droplet %d: %w", id, err)
	}
	status := dropletFromSDK(droplet)
	return &status, nil
}

func (b *sdkBackend) CreateDroplet(ctx context.Context, req CreateRequest) (int, error) {
	create := &godo.DropletCreateRequest{
		Name:     req.Name,
		Region:   req.Region,
		Size:     req.Size,
		Image:    godo.DropletCreateImage{Slug: req.Image},
		UserData: req.UserData,
	}
	for _, fingerprint := range req.SSHKeys {
		create.SSHKeys = append(create.SSHKeys, godo.DropletCreateSSHKey{Fingerprint: fingerprint})
	}
	droplet, _, err := b.client.Droplets.Create(ctx, create)
	if err != nil {
		// The request is not in the error: godo reports the response, and the
		// user_data it carried never reaches text.
		return 0, fmt.Errorf("create droplet %s: %w", req.Name, err)
	}
	if droplet == nil {
		return 0, fmt.Errorf("create droplet %s: DigitalOcean returned no droplet", req.Name)
	}
	return droplet.ID, nil
}

func (b *sdkBackend) PowerOn(ctx context.Context, id int) error {
	if _, _, err := b.client.DropletActions.PowerOn(ctx, id); err != nil {
		return fmt.Errorf("power on droplet %d: %w", id, err)
	}
	return nil
}

func (b *sdkBackend) PowerOff(ctx context.Context, id int) error {
	if _, _, err := b.client.DropletActions.PowerOff(ctx, id); err != nil {
		return fmt.Errorf("power off droplet %d: %w", id, err)
	}
	return nil
}

func (b *sdkBackend) Delete(ctx context.Context, id int) error {
	if _, err := b.client.Droplets.Delete(ctx, id); err != nil {
		return fmt.Errorf("destroy droplet %d: %w", id, err)
	}
	return nil
}

// dropletFromSDK names every field that leaves. Private and IPv6 networks,
// tags, volume ids, the VPC, kernel, backup and snapshot ids and feature flags
// are not copied.
func dropletFromSDK(d *godo.Droplet) DropletStatus {
	created, _ := time.Parse(time.RFC3339, d.Created)
	status := DropletStatus{
		ID:        d.ID,
		Name:      d.Name,
		Status:    d.Status,
		CreatedAt: created.UTC(),
	}
	if d.Region != nil {
		status.Region = d.Region.Slug
	}
	if d.Size != nil {
		status.Size = d.Size.Slug
	}
	if d.Image != nil {
		status.Image = d.Image.Slug
	}
	if d.Networks != nil {
		for _, network := range d.Networks.V4 {
			if network.Type == "public" {
				status.IPv4 = network.IPAddress
				break
			}
		}
	}
	return status
}
