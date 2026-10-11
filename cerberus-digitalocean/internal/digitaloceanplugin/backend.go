package digitaloceanplugin

import "context"

// Backend is everything this plugin asks of DigitalOcean. It returns this
// package's DTOs, never godo types, so the SDK is confined to sdk_backend.go
// and a connector built on Backend structurally cannot return a vendor struct.
type Backend interface {
	ListDroplets(ctx context.Context) (DropletList, error)
	GetDroplet(ctx context.Context, id int) (*DropletStatus, error)
	// CreateDroplet returns the new droplet's id. The caller reads the droplet
	// back with GetDroplet, as the compiled-in connector did.
	CreateDroplet(ctx context.Context, req CreateRequest) (int, error)
	PowerOn(ctx context.Context, id int) error
	PowerOff(ctx context.Context, id int) error
	Delete(ctx context.Context, id int) error

	FirewallBackend
	StorageBackend
	AccountBackend
}

// FirewallBackend is the cloud firewall surface. Ids are DigitalOcean's UUID
// strings; the plugin validates their shape before they reach the backend.
type FirewallBackend interface {
	ListFirewalls(ctx context.Context) (FirewallList, error)
	GetFirewall(ctx context.Context, id string) (*Firewall, error)
	CreateFirewall(ctx context.Context, spec FirewallSpec) (*Firewall, error)
	// UpdateFirewall replaces the whole firewall (DigitalOcean's PUT), so the
	// caller passes the complete desired state.
	UpdateFirewall(ctx context.Context, id string, spec FirewallSpec) (*Firewall, error)
	DeleteFirewall(ctx context.Context, id string) error
	AddFirewallDroplets(ctx context.Context, id string, dropletIDs []int) error
	RemoveFirewallDroplets(ctx context.Context, id string, dropletIDs []int) error
}

// CreateRequest is what create_droplet sends. It is ours, not godo's, so the
// SDK request type stays in sdk_backend.go with the rest of the SDK.
type CreateRequest struct {
	Name     string
	Region   string
	Size     string
	Image    string
	SSHKeys  []string
	UserData string
}

// StorageBackend is block storage volumes and snapshots, volume and droplet.
type StorageBackend interface{}

// AccountBackend is the rest of the fleet surface: SSH keys, tags, projects,
// reserved IPs and VPCs.
type AccountBackend interface{}
