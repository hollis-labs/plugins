package ghplugin

import "context"

// Backend is everything this plugin asks of GitHub. It returns this package's
// DTOs, so the HTTP client in client.go is the one place that talks to the
// GitHub API, and a connector built on Backend returns nothing it did not
// name.
type Backend interface {
	RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error)
	ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error)
	ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error)
}
