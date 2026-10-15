package ghplugin

import "time"

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): client.go decodes GitHub's responses into its
// own wire structs and maps them onto these field by field, so a field GitHub
// adds, or one it already sends and we do not name (owner and actor objects,
// permissions, URLs carrying tokens, head commits), is never emitted.
// dto_test.go holds that.
//
// Field names, JSON tags and untrusted labels match the compiled-in connector
// this plugin replaces, so the output is the same either side of the move.
// Text anyone with push access can set is labelled untrusted.

// RepoStatus is the normalized view of a GitHub repository's current state.
type RepoStatus struct {
	Owner       string    `json:"owner"`
	Repo        string    `json:"repo"`
	Description string    `json:"description,omitempty" cerb:"untrusted"`
	DefaultBr   string    `json:"default_branch"`
	Private     bool      `json:"private"`
	Stars       int       `json:"stars"`
	OpenIssues  int       `json:"open_issues"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Release is the normalized view of a GitHub release.
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name" cerb:"untrusted"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
}

// WorkflowRun is the normalized view of a GitHub Actions workflow run.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name" cerb:"untrusted"`
	Status     string    `json:"status"`     // queued, in_progress, completed
	Conclusion string    `json:"conclusion"` // success, failure, cancelled, etc.
	Branch     string    `json:"branch" cerb:"untrusted"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
	HTMLURL    string    `json:"html_url"`
}
