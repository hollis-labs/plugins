package vastaiplugin

import (
	"context"
	"time"
)

// Backend is everything this plugin asks of Vast.ai. It returns this package's
// DTOs, never decoded API maps, so the wire format is confined to client.go and
// a connector built on Backend structurally cannot return a vendor struct.
type Backend interface {
	GetAccount(ctx context.Context) (*Account, error)
	SearchOffers(ctx context.Context, q OfferQuery) ([]Offer, error)
	// GetOffer reads one offer by id. It returns (nil, nil) when the offer is
	// gone: someone else rented it, or the host withdrew it.
	GetOffer(ctx context.Context, id int) (*Offer, error)
	ListInstances(ctx context.Context) (InstanceList, error)
	GetInstance(ctx context.Context, id int) (*Instance, error)
	// CreateInstance returns the new instance's id. The caller reads the
	// instance back with GetInstance.
	CreateInstance(ctx context.Context, req CreateRequest) (int, error)
	// SetState moves an instance to "running" or "stopped".
	SetState(ctx context.Context, id int, state string) error
	SetLabel(ctx context.Context, id int, label string) error
	Destroy(ctx context.Context, id int) error
	// Charges returns billed charges per instance between from and to.
	Charges(ctx context.Context, from, to time.Time) (ChargeSet, error)
	Logs(ctx context.Context, id int, req LogsRequest) (LogsResult, error)
}

// OfferQuery is what search_offers sends. Zero values mean "no filter".
type OfferQuery struct {
	GPUNames       []string
	NumGPUs        int // exactly this many; 0 for any
	MinGPUs        int
	MinVRAMGB      float64 // per GPU
	MinTotalVRAMGB float64
	MaxDPH         float64 // $/hr ceiling for the whole offer
	MinReliability float64
	MinDiskGB      float64
	MinInetDown    float64 // Mbps
	Geolocation    []string
	Type           string // ondemand, bid, reserved
	Unverified     bool   // include machines Vast.ai has not verified
	OrderBy        string // a column name
	Descending     bool
	Limit          int
}

// CreateRequest is what create_instance sends.
type CreateRequest struct {
	OfferID     int
	Image       string
	Label       string
	DiskGB      float64
	Onstart     string
	Env         map[string]string
	Runtype     string
	TargetState string
}

// LogsRequest is what get_logs sends.
type LogsRequest struct {
	Tail   int
	Filter string
	Daemon bool
}

// Charge is one instance's billed charges over a window.
type Charge struct {
	InstanceID int
	Label      string
	Start      time.Time
	End        time.Time
	Total      float64
	GPU        float64
	Disk       float64
	Bandwidth  float64
}

// ChargeSet is every charge in a window. Truncated says the page cap cut it
// short.
type ChargeSet struct {
	Charges   []Charge
	Truncated bool
}
