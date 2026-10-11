package tailscaleplugin

import "time"

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): a field Tailscale adds is not emitted until it is
// named here. The mapping from the wire shapes lives in api_backend.go, the one
// file that decodes Tailscale JSON.
//
// Free text that a tailnet member controls (a device's hostname, a key's
// description) is labeled untrusted, so a client presents it as data and never
// as instructions. A device's user login is personal data.

// AuthKey is the normalized view of a key. It never carries the key value:
// Tailscale does not return it after creation, and only MintedKey does.
type AuthKey struct {
	ID            string     `json:"id"`
	KeyType       string     `json:"key_type,omitempty"` // auth, api, client
	Description   string     `json:"description,omitempty" cerb:"untrusted"`
	Created       *time.Time `json:"created,omitempty"`
	Expires       *time.Time `json:"expires,omitempty"`
	Revoked       *time.Time `json:"revoked,omitempty"`
	Invalid       bool       `json:"invalid"`
	Reusable      bool       `json:"reusable"`
	Ephemeral     bool       `json:"ephemeral"`
	Preauthorized bool       `json:"preauthorized"`
	Tags          []string   `json:"tags,omitempty"`
}

// AuthKeyList is list_auth_keys' result. Listing returns ids only, so each key
// is read back; the walk is capped (maxListedKeys) and Truncated says when the
// cap cut it short, so a partial inventory never looks complete.
type AuthKeyList struct {
	Keys      []AuthKey `json:"keys"`
	Truncated bool      `json:"truncated"`
}

// MintedKey is mint_auth_key's result: the key's attributes and its value, which
// is returned this once and cannot be read again.
type MintedKey struct {
	AuthKey
	Key  string `json:"key"`
	Note string `json:"note"`
}

// KeyAction reports a completed revoke.
type KeyAction struct {
	KeyID  string `json:"key_id"`
	Action string `json:"action"` // revoke
}

// Device is the normalized view of a tailnet device. The API carries much more
// (routes, endpoints, posture attributes, ...); none of it is returned.
type Device struct {
	ID                string     `json:"id"`
	NodeID            string     `json:"node_id,omitempty"`
	Name              string     `json:"name" cerb:"untrusted"`
	Hostname          string     `json:"hostname" cerb:"untrusted"`
	User              string     `json:"user,omitempty" cerb:"personal,untrusted"`
	OS                string     `json:"os,omitempty"`
	ClientVersion     string     `json:"client_version,omitempty"`
	Addresses         []string   `json:"addresses,omitempty"`
	Tags              []string   `json:"tags,omitempty"`
	Authorized        bool       `json:"authorized"`
	KeyExpiryDisabled bool       `json:"key_expiry_disabled"`
	UpdateAvailable   bool       `json:"update_available"`
	IsExternal        bool       `json:"is_external"`
	Created           *time.Time `json:"created,omitempty"`
	LastSeen          *time.Time `json:"last_seen,omitempty"`
	Expires           *time.Time `json:"expires,omitempty"`
}

// DeviceList is list_devices' result.
type DeviceList struct {
	Devices []Device `json:"devices"`
}

// DeviceAction reports a completed delete, authorize or set_tags.
type DeviceAction struct {
	DeviceID string   `json:"device_id"`
	Action   string   `json:"action"` // delete, authorize, set_tags
	Tags     []string `json:"tags,omitempty"`
}

// DryRunPreview is the preview a dry run returns. It has the same shape as the
// host's own previews, so the CLI and API render a plugin preview as they
// render the built-in's. The host cannot verify it: it is this plugin's claim
// of what the call would do.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
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
