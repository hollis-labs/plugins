package forgeplugin

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): Forge's responses are decoded into these structs,
// so a field Forge adds, or one it already sends and we do not name (a
// server's provider id, credentials, network details, a site's environment),
// is dropped at the decode and never emitted. dto_test.go holds that.
//
// Field names and JSON tags match the compiled-in connector this plugin
// replaces, and those tags are Forge's own field names, so the same struct
// serves both the decode and the output.

// Server is a Laravel Forge server.
type Server struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip_address"`
	Region   string `json:"region"`
	Size     string `json:"size"`
	PHP      string `json:"php_version"`
	Provider string `json:"provider"`
	IsReady  bool   `json:"is_ready"`
}

// Site is a site deployed on a Forge server.
type Site struct {
	ID               int    `json:"id"`
	ServerID         int    `json:"server_id"`
	Name             string `json:"name"`
	Directory        string `json:"directory"`
	Repository       string `json:"repository"`
	Branch           string `json:"deployment_branch"`
	Status           string `json:"status"`
	DeploymentStatus string `json:"deployment_status"`
}

// SiteCommand is a command execution record for a Forge site.
type SiteCommand struct {
	ID        int    `json:"id"`
	ServerID  int    `json:"server_id"`
	SiteID    int    `json:"site_id"`
	Command   string `json:"command"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// SiteAction reports a completed update_deployment_script or deploy_site. The
// compiled-in connector returned nothing for either; saying what was done is
// cheap and lets a caller tell a no-op from a success.
type SiteAction struct {
	ServerID int    `json:"server_id"`
	SiteID   int    `json:"site_id"`
	Action   string `json:"action"` // update_deployment_script, deploy
}

// DryRunPreview is the preview a dry run returns. It has the same shape as the
// host's own previews (ExternalConnectorDryRunPreview in Cerberus), so the CLI
// and API render a plugin preview exactly as they rendered the built-in's. The
// host cannot verify it: it is this plugin's claim of what the call would do.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
	// Diff is set on update_deployment_script's preview only.
	Diff *ScriptDiff `json:"diff,omitempty"`
}

// ScriptDiff compares a site's current deployment script with the proposed
// one, line by line.
type ScriptDiff struct {
	// Changed is false when the proposed script is identical to the current one.
	Changed bool `json:"changed"`
	Added   int  `json:"added"`
	Removed int  `json:"removed"`
	// Unified is the changed lines in unified-diff form, with a few lines of
	// context around each change. Empty when nothing changed.
	Unified string `json:"unified,omitempty"`
	// Truncated says the scripts were too large to diff line by line; the
	// counts are then a whole-script replacement and Unified is empty.
	Truncated bool `json:"truncated,omitempty"`
}

func newPreview(operation, summary string, target, input map[string]any, warnings ...string) DryRunPreview {
	return DryRunPreview{
		DryRun:    true,
		Connector: ConnectorID,
		Operation: operation,
		Summary:   summary,
		Target:    target,
		Input:     input,
		Warnings:  warnings,
	}
}
