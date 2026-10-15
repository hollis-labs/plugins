package forgeplugin

import "context"

// Backend is everything this plugin asks of Laravel Forge. It returns this
// package's DTOs, so the HTTP client in client.go is the one place that talks
// to the Forge API, and a connector built on Backend returns nothing it did
// not name.
type Backend interface {
	ListServers(ctx context.Context) ([]Server, error)
	GetServer(ctx context.Context, serverID int) (*Server, error)
	ListSites(ctx context.Context, serverID int) ([]Site, error)
	GetDeploymentScript(ctx context.Context, serverID, siteID int) (string, error)
	UpdateDeploymentScript(ctx context.Context, serverID, siteID int, content string, autoSource bool) error
	DeploySite(ctx context.Context, serverID, siteID int) error
	ExecuteSiteCommand(ctx context.Context, serverID, siteID int, command string) (*SiteCommand, error)
}
