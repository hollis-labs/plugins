package digitaloceanplugin

import (
	"context"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
)

func (b *sdkBackend) ListFirewalls(ctx context.Context) (FirewallList, error) {
	out := FirewallList{Firewalls: []Firewall{}}
	opt := &godo.ListOptions{Page: 1, PerPage: listPageSize}
	for {
		firewalls, resp, err := b.client.Firewalls.List(ctx, opt)
		if err != nil {
			return FirewallList{}, fmt.Errorf("list firewalls: %w", err)
		}
		for i := range firewalls {
			out.Firewalls = append(out.Firewalls, firewallFromSDK(&firewalls[i]))
		}
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() || len(firewalls) == 0 {
			return out, nil
		}
		if opt.Page >= maxListPages {
			out.Truncated = true
			return out, nil
		}
		opt.Page++
	}
}

func (b *sdkBackend) GetFirewall(ctx context.Context, id string) (*Firewall, error) {
	fw, _, err := b.client.Firewalls.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get firewall %s: %w", id, err)
	}
	out := firewallFromSDK(fw)
	return &out, nil
}

func (b *sdkBackend) CreateFirewall(ctx context.Context, spec FirewallSpec) (*Firewall, error) {
	fw, _, err := b.client.Firewalls.Create(ctx, firewallRequestFromSpec(spec))
	if err != nil {
		return nil, fmt.Errorf("create firewall %s: %w", spec.Name, err)
	}
	out := firewallFromSDK(fw)
	return &out, nil
}

func (b *sdkBackend) UpdateFirewall(ctx context.Context, id string, spec FirewallSpec) (*Firewall, error) {
	fw, _, err := b.client.Firewalls.Update(ctx, id, firewallRequestFromSpec(spec))
	if err != nil {
		return nil, fmt.Errorf("update firewall %s: %w", id, err)
	}
	out := firewallFromSDK(fw)
	return &out, nil
}

func (b *sdkBackend) DeleteFirewall(ctx context.Context, id string) error {
	if _, err := b.client.Firewalls.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete firewall %s: %w", id, err)
	}
	return nil
}

func (b *sdkBackend) AddFirewallDroplets(ctx context.Context, id string, dropletIDs []int) error {
	if _, err := b.client.Firewalls.AddDroplets(ctx, id, dropletIDs...); err != nil {
		return fmt.Errorf("attach droplets to firewall %s: %w", id, err)
	}
	return nil
}

func (b *sdkBackend) RemoveFirewallDroplets(ctx context.Context, id string, dropletIDs []int) error {
	if _, err := b.client.Firewalls.RemoveDroplets(ctx, id, dropletIDs...); err != nil {
		return fmt.Errorf("detach droplets from firewall %s: %w", id, err)
	}
	return nil
}

// firewallRequestFromSpec maps our spec onto godo. Nil slices become empty
// ones: DigitalOcean's update is a full replace, and a null rule list is not
// the same request as an empty one.
func firewallRequestFromSpec(spec FirewallSpec) *godo.FirewallRequest {
	req := &godo.FirewallRequest{
		Name:          spec.Name,
		InboundRules:  []godo.InboundRule{},
		OutboundRules: []godo.OutboundRule{},
		DropletIDs:    append([]int{}, spec.DropletIDs...),
		Tags:          append([]string{}, spec.Tags...),
	}
	for _, r := range spec.InboundRules {
		req.InboundRules = append(req.InboundRules, godo.InboundRule{
			Protocol: r.Protocol, PortRange: r.Ports,
			Sources: &godo.Sources{Addresses: r.Addresses, Tags: r.Tags, DropletIDs: r.DropletIDs, LoadBalancerUIDs: r.LoadBalancerUIDs, KubernetesIDs: r.KubernetesIDs},
		})
	}
	for _, r := range spec.OutboundRules {
		req.OutboundRules = append(req.OutboundRules, godo.OutboundRule{
			Protocol: r.Protocol, PortRange: r.Ports,
			Destinations: &godo.Destinations{Addresses: r.Addresses, Tags: r.Tags, DropletIDs: r.DropletIDs, LoadBalancerUIDs: r.LoadBalancerUIDs, KubernetesIDs: r.KubernetesIDs},
		})
	}
	return req
}

// firewallFromSDK names every field that leaves. Pending changes (which carry
// per-droplet status) are not copied.
func firewallFromSDK(fw *godo.Firewall) Firewall {
	created, _ := time.Parse(time.RFC3339, fw.Created)
	out := Firewall{
		ID: fw.ID, Name: fw.Name, Status: fw.Status,
		InboundRules:  []FirewallRule{},
		OutboundRules: []FirewallRule{},
		DropletIDs:    append([]int{}, fw.DropletIDs...),
		Tags:          append([]string{}, fw.Tags...),
		CreatedAt:     created.UTC(),
	}
	for _, r := range fw.InboundRules {
		rule := FirewallRule{Protocol: r.Protocol, Ports: r.PortRange}
		if s := r.Sources; s != nil {
			rule.Addresses, rule.Tags, rule.DropletIDs, rule.LoadBalancerUIDs, rule.KubernetesIDs = s.Addresses, s.Tags, s.DropletIDs, s.LoadBalancerUIDs, s.KubernetesIDs
		}
		out.InboundRules = append(out.InboundRules, rule)
	}
	for _, r := range fw.OutboundRules {
		rule := FirewallRule{Protocol: r.Protocol, Ports: r.PortRange}
		if d := r.Destinations; d != nil {
			rule.Addresses, rule.Tags, rule.DropletIDs, rule.LoadBalancerUIDs, rule.KubernetesIDs = d.Addresses, d.Tags, d.DropletIDs, d.LoadBalancerUIDs, d.KubernetesIDs
		}
		out.OutboundRules = append(out.OutboundRules, rule)
	}
	out.WorldOpen = hasWorldOpen(out.InboundRules)
	return out
}
