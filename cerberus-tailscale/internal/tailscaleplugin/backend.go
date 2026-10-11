package tailscaleplugin

import (
	"context"
	"time"
)

// Backend is everything this plugin asks of the Tailscale control-plane API.
// It returns this package's DTOs, never decoded vendor JSON, so the wire shapes
// are confined to api_backend.go and a connector built on Backend structurally
// cannot return a vendor struct. Tests run against a fake Backend, or against
// the real HTTP backend pointed at a fake server.
//
// Auth keys are tailnet-scoped, so those calls take the tailnet. Devices are
// addressed by id alone.
type Backend interface {
	ListAuthKeys(ctx context.Context, tailnet string) (AuthKeyList, error)
	GetAuthKey(ctx context.Context, tailnet, id string) (*AuthKey, error)
	// CreateAuthKey returns the new key including its value, which Tailscale
	// shows only in this response.
	CreateAuthKey(ctx context.Context, tailnet string, req MintRequest) (*MintedKey, error)
	RevokeAuthKey(ctx context.Context, tailnet, id string) error

	ListDevices(ctx context.Context, tailnet string) (DeviceList, error)
	GetDevice(ctx context.Context, id string) (*Device, error)
	DeleteDevice(ctx context.Context, id string) error
	SetDeviceTags(ctx context.Context, id string, tags []string) error
	AuthorizeDevice(ctx context.Context, id string) error
}

// MintRequest is what mint_auth_key sends. It is ours, not the wire body.
type MintRequest struct {
	Description   string
	Tags          []string
	Reusable      bool
	Ephemeral     bool
	Preauthorized bool
	TTL           time.Duration
}
