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
	Content  string `json:"content" cerb:"untrusted"`
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

// TokenStatus is verify_token's result. Policies is what the token holds, by
// permission group name; it is only present when Cloudflare let the token read
// itself (PoliciesAvailable), which needs an API Tokens read permission that a
// least-privilege token usually does not have. Group ids and request
// conditions on the token are not returned.
type TokenStatus struct {
	ID                string        `json:"id"`
	Status            string        `json:"status"`
	Scope             string        `json:"scope"` // user or account
	ExpiresOn         string        `json:"expires_on,omitempty"`
	NotBefore         string        `json:"not_before,omitempty"`
	PoliciesAvailable bool          `json:"policies_available"`
	Policies          []TokenPolicy `json:"policies"`
	Note              string        `json:"note,omitempty"`
}

// TokenPolicy is one policy on a token: an effect, the permission groups it
// grants and the resources they apply to.
type TokenPolicy struct {
	Effect           string   `json:"effect"`
	PermissionGroups []string `json:"permission_groups"`
	Resources        []string `json:"resources"`
}

// Bucket is an R2 bucket. Names are chosen by whoever created the bucket, so
// the name is labelled untrusted.
type Bucket struct {
	Name         string `json:"name" cerb:"untrusted"`
	CreatedAt    string `json:"created_at,omitempty"`
	Location     string `json:"location,omitempty"`
	StorageClass string `json:"storage_class,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

// BucketDeleted reports a completed delete_r2_bucket.
type BucketDeleted struct {
	Deleted      bool   `json:"deleted"`
	AccountID    string `json:"account_id"`
	Name         string `json:"name"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
}

// R2Credential is create_r2_credential's result: an S3-compatible key pair for
// one bucket. The secret half is returned this once. Cloudflare derives it from
// the API token it creates and shows that token only in the creation response,
// so it cannot be read again; this plugin does not store it, log it or return
// the API token it came from.
//
// The secret half is named so the host's redactor, which rewrites any field
// whose name says secret, token or password, leaves it alone: it is the one
// value this operation exists to hand over.
type R2Credential struct {
	AccessKeyID    string `json:"access_key_id"`
	AccessKeyValue string `json:"access_key_value"`
	Bucket         string `json:"bucket"`
	Permission     string `json:"permission"`
	Endpoint       string `json:"endpoint"`
	Note           string `json:"note"`
}

// CredentialRequest is what create_r2_credential sends. It is ours, not the
// wire body.
type CredentialRequest struct {
	Bucket       string
	Jurisdiction string
	Permission   string // read_write or read
}

// CredentialInfo is what revoke_r2_credential reads before it revokes: enough
// to tell a key this plugin minted from any other token.
type CredentialInfo struct {
	AccessKeyID string `json:"access_key_id"`
	Name        string `json:"name" cerb:"untrusted"`
	Status      string `json:"status"`
}

// CredentialRevoked reports a completed revoke_r2_credential.
type CredentialRevoked struct {
	Revoked     bool   `json:"revoked"`
	AccountID   string `json:"account_id"`
	AccessKeyID string `json:"access_key_id"`
}
