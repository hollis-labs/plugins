package digitaloceanplugin

import (
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// Fleet-administration shapes (firewalls, volumes, snapshots, keys, tags,
// projects, reserved IPs, VPCs). Same rule as dto.go: each is an allow-list,
// and the godo mapping lives in the sdk_*.go files.
//
// Free text an operator or a third party can write (names, descriptions) is
// labelled untrusted, so a client presents it as data and never as an
// instruction.

// FirewallRule is one inbound or outbound rule. For an inbound rule the
// addresses, tags and droplet ids are sources; for an outbound rule they are
// destinations.
type FirewallRule struct {
	Protocol         string   `json:"protocol"` // tcp, udp, icmp
	Ports            string   `json:"ports,omitempty"`
	Addresses        []string `json:"addresses,omitempty"`
	Tags             []string `json:"tags,omitempty" cerb:"untrusted"`
	DropletIDs       []int    `json:"droplet_ids,omitempty"`
	LoadBalancerUIDs []string `json:"load_balancer_uids,omitempty"`
	KubernetesIDs    []string `json:"kubernetes_ids,omitempty"`
}

// Firewall is the normalized view of a cloud firewall. WorldOpen is computed by
// this plugin, not read from DigitalOcean: true when any inbound rule admits
// 0.0.0.0/0 or ::/0, so an audit of list_firewalls shows the dangerous ones at
// a glance. Pending changes and status details are not returned.
type Firewall struct {
	ID            string         `json:"id"`
	Name          string         `json:"name" cerb:"untrusted"`
	Status        string         `json:"status"`
	InboundRules  []FirewallRule `json:"inbound_rules"`
	OutboundRules []FirewallRule `json:"outbound_rules"`
	DropletIDs    []int          `json:"droplet_ids"`
	Tags          []string       `json:"tags" cerb:"untrusted"`
	CreatedAt     time.Time      `json:"created_at"`
	WorldOpen     bool           `json:"world_open"`
}

// FirewallList is list_firewalls' result; Truncated says the page cap cut it.
type FirewallList struct {
	Firewalls []Firewall `json:"firewalls"`
	Truncated bool       `json:"truncated"`
}

// FirewallSpec is the desired state a create or update sends.
type FirewallSpec struct {
	Name          string
	InboundRules  []FirewallRule
	OutboundRules []FirewallRule
	DropletIDs    []int
	Tags          []string
}

// ChangeResult reports a completed write that has no richer result: a delete,
// an attach, an assignment. Resource names the kind, ID the thing changed.
type ChangeResult struct {
	Resource  string `json:"resource"`
	ID        string `json:"id"`
	Action    string `json:"action"`
	DropletID int    `json:"droplet_id,omitempty"`
	// Detail carries counts or ids affected (for example the droplets added to
	// a firewall), never free text.
	Detail []string `json:"detail,omitempty"`
}

// worldCIDRs are the two prefixes that admit the whole internet.
const (
	worldIPv4 = "0.0.0.0/0"
	worldIPv6 = "::/0"
)

// isWorldAddress reports whether addr is a /0 prefix of either family. Parsing
// rather than string comparison means "0.0.0.0/0" spelled with whitespace or
// an IPv4-mapped form is still caught.
func isWorldAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	prefix, err := netip.ParsePrefix(addr)
	return err == nil && prefix.Bits() == 0
}

// worldOpenFindings names every inbound rule that admits the whole internet,
// one line each, for a preview to put first.
func worldOpenFindings(rules []FirewallRule) []string {
	var out []string
	for i, rule := range rules {
		for _, addr := range rule.Addresses {
			if !isWorldAddress(addr) {
				continue
			}
			ports := rule.Ports
			switch ports {
			case "", "0", "all":
				ports = "ALL PORTS"
			default:
				ports = "port " + ports
			}
			out = append(out, fmt.Sprintf("inbound rule %d (%s, %s) accepts traffic from ANYWHERE on the internet (%s)",
				i+1, rule.Protocol, ports, strings.TrimSpace(addr)))
		}
	}
	return out
}

func hasWorldOpen(rules []FirewallRule) bool { return len(worldOpenFindings(rules)) > 0 }

// withWarnings adds warnings to a preview.
func (p DryRunPreview) withWarnings(warnings ...string) DryRunPreview {
	p.Warnings = append(p.Warnings, warnings...)
	return p
}

// tailnetOnlyPosture is the standing reminder attached to every firewall
// preview that opens the world. It is the same sentence as the operation
// descriptions: invariant H2 in the Cerberus compute-hosting plan.
const tailnetOnlyPosture = "Intended posture is tailnet-only (invariant H2, docs/plans/compute-hosting.md): the provider firewall denies all public inbound and services are reached over the tailnet. Proceed only if public exposure is deliberate."

// Volume is the normalized view of a block storage volume.
type Volume struct {
	ID             string    `json:"id"`
	Name           string    `json:"name" cerb:"untrusted"`
	Region         string    `json:"region"`
	SizeGigabytes  int64     `json:"size_gigabytes"`
	Description    string    `json:"description,omitempty" cerb:"untrusted"`
	DropletIDs     []int     `json:"droplet_ids"` // droplets it is attached to
	FilesystemType string    `json:"filesystem_type,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Tags           []string  `json:"tags" cerb:"untrusted"`
}

// VolumeList is list_volumes' result.
type VolumeList struct {
	Volumes   []Volume `json:"volumes"`
	Truncated bool     `json:"truncated"`
}

// VolumeSpec is what create_volume sends.
type VolumeSpec struct {
	Name          string
	Region        string
	SizeGigabytes int64
	Description   string
	SnapshotID    string
}

// Snapshot is a volume or droplet snapshot.
type Snapshot struct {
	ID            string    `json:"id"`
	Name          string    `json:"name" cerb:"untrusted"`
	ResourceID    string    `json:"resource_id,omitempty"`
	ResourceType  string    `json:"resource_type,omitempty"` // droplet, volume
	Regions       []string  `json:"regions,omitempty"`
	MinDiskSizeGB int       `json:"min_disk_size_gigabytes,omitempty"`
	SizeGigabytes float64   `json:"size_gigabytes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	Tags          []string  `json:"tags,omitempty" cerb:"untrusted"`
}

// SnapshotList is list_snapshots' result.
type SnapshotList struct {
	Snapshots []Snapshot `json:"snapshots"`
	Truncated bool       `json:"truncated"`
}

// SnapshotStarted reports a droplet snapshot that was accepted. The snapshot
// is taken asynchronously and shows up in list_snapshots when it finishes.
type SnapshotStarted struct {
	DropletID int    `json:"droplet_id"`
	Name      string `json:"name" cerb:"untrusted"`
	Action    string `json:"action"` // snapshot
}
