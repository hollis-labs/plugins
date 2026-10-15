package vercelplugin

// Status is what status reports. It names what is configured, never a value
// of a credential.
type Status struct {
	VercelCLI        string   `json:"vercel_cli,omitempty"`
	TokenConfigured  bool     `json:"token_configured"`
	ScopeConfigured  bool     `json:"scope_configured"`
	ProfilesFile     string   `json:"profiles_file,omitempty"`
	Profiles         int      `json:"profiles"`
	Problems         []string `json:"problems"`
	ProfilesFileRead bool     `json:"profiles_file_read"`
}

// ProfileSummary is a profile as list_profiles shows it: what it deploys and
// where, without its commands. A command is operator-written text that can
// embed a secret, so it appears only in a deploy's dry run and run.
type ProfileSummary struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	RepoPath      string `json:"repo_path"`
	VercelProject string `json:"vercel_project,omitempty"`
	VercelScope   string `json:"vercel_scope,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Linked        bool   `json:"linked"`
}

// PlannedStep is one command a deploy runs, as the operator is shown it
// before confirming. A secret never appears in it — the token is named, in
// the command's VERCEL_TOKEN=<vercel token> prefix and in Env — so the plan is
// safe to display and to record.
type PlannedStep struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	// Env names the variables the step is given, never their values.
	Env []string `json:"env,omitempty"`
}

// GitStatus is the checkout a deploy runs from.
type GitStatus struct {
	Branch    string   `json:"branch,omitempty" cerb:"untrusted"`
	Commit    string   `json:"commit,omitempty"`
	Dirty     bool     `json:"dirty"`
	Remotes   []string `json:"remotes,omitempty" cerb:"untrusted"`
	RemoteURL string   `json:"remote_url,omitempty" cerb:"untrusted"`
}

// DryRunPreview is the preview a dry run returns. It has the host's preview
// shape (ExternalConnectorDryRunPreview in Cerberus), plus the plan: the
// steps, the profile's digest and the checkout. The host binds its plan hash
// to this JSON, so a profile or checkout that changes between the preview and
// an approved run makes the approval stale. It is this plugin's claim, which
// the host cannot verify.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`

	RepoPath      string        `json:"repo_path"`
	ProfileSHA256 string        `json:"profile_sha256"`
	Steps         []PlannedStep `json:"steps"`
	Git           GitStatus     `json:"git"`
}

// RunResult is what a deploy did.
type RunResult struct {
	Success       bool      `json:"success"`
	Profile       string    `json:"profile"`
	DeploymentURL string    `json:"deployment_url,omitempty" cerb:"untrusted"`
	Git           GitStatus `json:"git"`
	Steps         []StepRun `json:"steps"`
	Error         string    `json:"error,omitempty" cerb:"untrusted"`
}

// StepRun is one step's outcome. Output is the step's combined stdout and
// stderr, keeping its tail when it is long.
type StepRun struct {
	Name            string `json:"name"`
	Command         string `json:"command"`
	Success         bool   `json:"success"`
	Output          string `json:"output,omitempty" cerb:"untrusted"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Error           string `json:"error,omitempty" cerb:"untrusted"`
}
