package cloudflareplugin

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): a field the vendor adds is not emitted until it
// is named here. The mapping from cloudflare-go lives in sdk_backend.go, the
// one file that imports the SDK, so a reviewer sees the whole boundary in one
// place.
//
// Field names and JSON tags match the compiled-in connector this plugin
// replaces, so output is unchanged for anyone reading it.

// Zone is a Cloudflare zone. The SDK type also carries the owning account and
// the owner's name; neither is returned.
type Zone struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Paused      bool     `json:"paused"`
	NameServers []string `json:"name_servers"`
	Plan        string   `json:"plan"`
}

// DNSRecord is a DNS record. Comments, tags and metadata on the SDK type are
// not returned.
type DNSRecord struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
	Priority *int   `json:"priority,omitempty"`
}

// DeletedRecord reports a completed delete_dns_record.
type DeletedRecord struct {
	Deleted  bool   `json:"deleted"`
	ZoneID   string `json:"zone_id"`
	RecordID string `json:"record_id"`
}

// DryRunPreview is the preview a dry run returns instead of calling
// Cloudflare. It has the same shape as the host's own previews
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
