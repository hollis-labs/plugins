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
