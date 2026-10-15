package caddyplugin

// These are the only shapes render and apply return (ADR 0003): allow-lists,
// built by hand from the validated input and the files this plugin read. None
// carries the Cloudflare token or any value derived from it; the Caddyfile
// refers to the token by Caddy placeholder only.

// RenderedRoute is one declared route as it appears in the Caddyfile.
type RenderedRoute struct {
	Name     string `json:"name"`
	FQDN     string `json:"fqdn"`
	Upstream string `json:"upstream"`
}

// RenderResult is what render returns.
type RenderResult struct {
	ConfigPath string          `json:"config_path"`
	Bind       string          `json:"bind"`
	TLS        string          `json:"tls"`
	Routes     []RenderedRoute `json:"routes"`
	// Caddyfile is composed entirely from validated input.
	Caddyfile string `json:"caddyfile"`
	// CurrentExists reports whether a file already exists at ConfigPath.
	CurrentExists bool `json:"current_exists"`
	Changed       bool `json:"changed"`
	// Diff is a unified diff from the current file to Caddyfile. It quotes the
	// current file, which this plugin did not write, so it is untrusted text.
	Diff string `json:"diff" cerb:"untrusted"`
	// TokenPlaceholder is the Caddy placeholder the file uses for the DNS-01
	// token, or empty when the TLS mode needs none.
	TokenPlaceholder string   `json:"token_placeholder,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// ApplyResult is what apply returns, for a preview and for a real run.
type ApplyResult struct {
	DryRun     bool   `json:"dry_run"`
	ConfigPath string `json:"config_path"`
	// Changed reports whether the rendered file differs from the current one.
	Changed bool `json:"changed"`
	// Applied is true only when the new file is written, validated and
	// reloaded and stayed in place.
	Applied    bool `json:"applied"`
	Validated  bool `json:"validated"`
	Reloaded   bool `json:"reloaded"`
	RolledBack bool `json:"rolled_back"`
	// Summary says what happened in a sentence this plugin composed.
	Summary string `json:"summary"`
	// Diff is the unified diff from the previous file to the new one.
	Diff string `json:"diff" cerb:"untrusted"`
	// CaddyOutput is what caddy printed on a failed validate or reload.
	CaddyOutput string   `json:"caddy_output,omitempty" cerb:"untrusted"`
	Warnings    []string `json:"warnings,omitempty"`
}
