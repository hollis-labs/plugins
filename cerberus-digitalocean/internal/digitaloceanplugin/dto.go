package digitaloceanplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): a field the vendor adds is not emitted until it
// is named here. The mapping from godo lives in sdk_backend.go, the one file
// that imports the SDK, so a reviewer sees the whole boundary in one place.
//
// Field names and JSON tags match the compiled-in connector this plugin
// replaces, so output is unchanged for anyone reading it.

// DropletStatus is the normalized view of a droplet. The SDK type also carries
// private and IPv6 networks, tags, volume ids, the VPC, kernel, backup and
// snapshot ids and feature flags; none of them is returned.
type DropletStatus struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"` // new, active, off, archive
	Region    string    `json:"region"`
	Size      string    `json:"size"`
	Image     string    `json:"image"`
	IPv4      string    `json:"ipv4,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// CreatedReference is what create_droplet returns when the droplet was
// created but reading it back failed. The create succeeded and is billable,
// so it is reported as a success carrying the id rather than as an error that
// would invite a retry and a second droplet. The compiled-in connector
// returned the same shape.
type CreatedReference struct {
	DropletID int `json:"droplet_id"`
}

// DropletAction reports a completed start, stop or destroy.
type DropletAction struct {
	DropletID int    `json:"droplet_id"`
	Action    string `json:"action"` // power_on, power_off, destroy
}

// DryRunPreview is the preview a dry run returns instead of calling
// DigitalOcean. It has the same shape as the host's own previews
// (ExternalConnectorDryRunPreview in Cerberus), so the CLI and API render a
// plugin preview exactly as they rendered the built-in's. The host cannot
// verify it: it is this plugin's claim of what the call would do.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

func newPreview(operation, summary string, target, input map[string]any) DryRunPreview {
	return DryRunPreview{
		DryRun:    true,
		Connector: ConnectorID,
		Operation: operation,
		Summary:   summary,
		Target:    target,
		Input:     input,
	}
}

// userDataDigest describes cloud-init user_data without carrying it. The
// script routinely holds credentials, and a preview lands in agent context and
// logs, so it shows only enough to confirm which script would run: its size
// and hash. It matches the host's userDataDigest exactly, so the preview reads
// the same as the compiled-in connector's. Compare with `shasum -a 256 <file>`.
func userDataDigest(userData string) map[string]any {
	if userData == "" {
		return map[string]any{"bytes": 0}
	}
	sum := sha256.Sum256([]byte(userData))
	return map[string]any{
		"bytes":  len(userData),
		"sha256": hex.EncodeToString(sum[:]),
	}
}
