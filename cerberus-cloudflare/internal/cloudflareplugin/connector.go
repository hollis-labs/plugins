package cloudflareplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// It is deliberately the id of the connector Cerberus used to compile in. The
// host resolves a plugin's secrets as <plugin id>/<secret name>, which is the
// key the built-in read, so CERBERUS_CLOUDFLARE_API_TOKEN, a `cloudflare:`
// entry in connector-secrets.yaml and keychain://cloudflare/api_token all keep
// working with no migration. The same fact means the host refuses to install
// this plugin while the built-in is still registered: the id is reserved until
// the built-in is removed.
const ConnectorID = "cloudflare"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.3.0"

// SecretAPIToken is the manifest secret name, and the key the host uses in the
// init config it hands us. It must stay "api_token": that is the name existing
// credential references were written against.
const SecretAPIToken = "api_token"

// TokenEnvVar is the variable the host itself resolves for SecretAPIToken
// (CERBERUS_<ID>_<NAME>), and the one this binary reads when run directly,
// outside the host.
const TokenEnvVar = "CERBERUS_CLOUDFLARE_API_TOKEN"

// ZoneTypeFull and ZoneTypePartial are the zone types create_zone accepts.
const (
	ZoneTypeFull    = "full"
	ZoneTypePartial = "partial"
)

var jurisdictionArg = contract.StringSchema("R2 jurisdiction: default (the default), eu or fedramp.")

// Definition declares what this connector does. The operations, their input
// schemas and their effects match the compiled-in connector
// this plugin replaces, so a caller sees the same contract either side of the
// move.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Domain)},
		Capabilities: contract.Capabilities{
			CanCreate:  true,
			CanDestroy: true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        "zone_id",
					Type:        "string",
					Description: "Cloudflare zone ID.",
				},
				{
					Name:        "account_id",
					Type:        "string",
					Description: "Cloudflare account ID used for zone creation.",
				},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        SecretAPIToken,
				Description: "Cloudflare API token. A token scoped to Zone Read and DNS Read is enough for the read operations.",
				Env:         TokenEnvVar,
				Required:    true,
			}},
		},
		// The contract matches the compiled-in connector's, except the
		// preview: here it is the plugin's claim, which the host cannot verify.
		// Finalize derives destructive, requires_ack and supports_dry from it.
		Operations: []contract.Operation{
			{
				Name:         "verify_token",
				OutputSchema: contract.OutputSchemaOf[TokenStatus](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.token"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Verify the API token this plugin holds: its status, expiry and, when Cloudflare lets the token read itself, the permission groups it grants. Use account_id for an account-owned token.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id": contract.StringSchema("Cloudflare account ID, for an account-owned token. Omit for a user-owned token."),
				}),
				Examples: []string{
					"cerberus connectors exec cloudflare verify_token",
					"cerberus connectors exec cloudflare verify_token --arg account_id=<account-id>",
				},
			},
			{
				Name:         "list_zones",
				OutputSchema: contract.OutputSchemaOf[[]Zone](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.account"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List Cloudflare zones.",
				InputSchema:  contract.ObjectSchema(map[string]any{}),
				Examples:     []string{"cerberus connectors exec cloudflare list_zones"},
			},
			{
				Name:         "create_zone",
				OutputSchema: contract.OutputSchemaOf[Zone](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.account", From: []string{"account_id"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Create a Cloudflare zone in an account.",
				Examples: []string{
					"cerberus connectors exec cloudflare create_zone --arg account_id=<account-id> --arg name=example.com --arg type=full --dry-run",
					"cerberus connectors exec cloudflare create_zone --arg account_id=<account-id> --arg name=example.com --arg type=full --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id": contract.StringSchema("Cloudflare account ID."),
					"name":       contract.StringSchema("Zone name such as example.com."),
					"type":       contract.StringSchema("Zone type: full or partial."),
				}, "account_id", "name"),
			},
			{
				Name:         "list_dns_records",
				OutputSchema: contract.OutputSchemaOf[[]DNSRecord](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.zone", From: []string{"zone_id"}},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List DNS records for a Cloudflare zone.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id": contract.StringSchema("Cloudflare zone ID."),
				}, "zone_id"),
				Examples: []string{"cerberus connectors exec cloudflare list_dns_records --arg zone_id=<zone-id>"},
			},
			{
				Name:         "create_dns_record",
				OutputSchema: contract.OutputSchemaOf[DNSRecord](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.zone", From: []string{"zone_id"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Create a DNS record in a Cloudflare zone.",
				Examples: []string{
					"cerberus connectors exec cloudflare create_dns_record --arg zone_id=<zone-id> --arg type=A --arg name=app --arg content=203.0.113.10 --arg ttl=300 --dry-run",
					"cerberus connectors exec cloudflare create_dns_record --arg zone_id=<zone-id> --arg type=CNAME --arg name=www --arg content=app.example.com --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id":  contract.StringSchema("Cloudflare zone ID."),
					"type":     contract.StringSchema("DNS record type."),
					"name":     contract.StringSchema("DNS record name."),
					"content":  contract.StringSchema("DNS record content."),
					"ttl":      contract.IntegerSchema("TTL in seconds."),
					"proxied":  map[string]any{"type": "boolean", "description": "Whether to proxy the record through Cloudflare."},
					"priority": contract.IntegerSchema("Priority for MX records."),
				}, "zone_id", "type", "name", "content"),
			},
			{
				Name:         "delete_dns_record",
				OutputSchema: contract.OutputSchemaOf[DeletedRecord](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.dns_record", From: []string{"zone_id", "record_id"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Delete a DNS record from a Cloudflare zone.",
				Examples: []string{
					"cerberus connectors exec cloudflare delete_dns_record --arg zone_id=<zone-id> --arg record_id=<record-id> --dry-run",
					"cerberus connectors exec cloudflare delete_dns_record --arg zone_id=<zone-id> --arg record_id=<record-id> --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"zone_id":   contract.StringSchema("Cloudflare zone ID."),
					"record_id": contract.StringSchema("Cloudflare DNS record ID."),
				}, "zone_id", "record_id"),
			},
			{
				Name:         "list_r2_buckets",
				OutputSchema: contract.OutputSchemaOf[[]Bucket](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.account", From: []string{"account_id"}},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List the R2 buckets in an account.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id":   contract.StringSchema("Cloudflare account ID."),
					"jurisdiction": jurisdictionArg,
				}, "account_id"),
				Examples: []string{"cerberus connectors exec cloudflare list_r2_buckets --arg account_id=<account-id>"},
			},
			{
				Name:         "create_r2_bucket",
				OutputSchema: contract.OutputSchemaOf[Bucket](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.r2_bucket", From: []string{"account_id", "name"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Create an R2 bucket. R2 storage and operations are billed by use.",
				Examples: []string{
					"cerberus connectors exec cloudflare create_r2_bucket --arg account_id=<account-id> --arg name=example-backups --dry-run",
					"cerberus connectors exec cloudflare create_r2_bucket --arg account_id=<account-id> --arg name=example-backups --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id":    contract.StringSchema("Cloudflare account ID."),
					"name":          contract.StringSchema("Bucket name: 3 to 63 characters, lowercase letters, digits and hyphens, starting and ending with a letter or digit."),
					"jurisdiction":  jurisdictionArg,
					"location_hint": contract.StringSchema("Where to place the bucket: apac, eeur, enam, weur, wnam or oc. Optional; Cloudflare chooses without one."),
					"storage_class": contract.StringSchema("Standard (the default) or InfrequentAccess."),
				}, "account_id", "name"),
			},
			{
				Name:         "delete_r2_bucket",
				OutputSchema: contract.OutputSchemaOf[BucketDeleted](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.r2_bucket", From: []string{"account_id", "name"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Delete an R2 bucket. Cloudflare refuses while the bucket still holds objects; this plugin cannot list objects, so it relies on that refusal and never empties a bucket.",
				Examples: []string{
					"cerberus connectors exec cloudflare delete_r2_bucket --arg account_id=<account-id> --arg name=example-backups --dry-run",
					"cerberus connectors exec cloudflare delete_r2_bucket --arg account_id=<account-id> --arg name=example-backups --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id":   contract.StringSchema("Cloudflare account ID."),
					"name":         contract.StringSchema("Bucket name."),
					"jurisdiction": jurisdictionArg,
				}, "account_id", "name"),
			},
			{
				Name:         "create_r2_credential",
				OutputSchema: contract.OutputSchemaOf[R2Credential](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.r2_bucket", From: []string{"account_id", "bucket"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Mint an S3 key pair limited to one R2 bucket and to object read and write (or read only). This is a credential mint: the secret half is returned once and cannot be read again, and this plugin does not store or log it. Needs a token that can create account API tokens.",
				Examples: []string{
					"cerberus connectors exec cloudflare create_r2_credential --arg account_id=<account-id> --arg bucket=example-backups --dry-run",
					"cerberus connectors exec cloudflare create_r2_credential --arg account_id=<account-id> --arg bucket=example-backups --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id":   contract.StringSchema("Cloudflare account ID."),
					"bucket":       contract.StringSchema("The one bucket the key may reach. It must exist."),
					"permission":   contract.StringSchema("Object access: read_write (the default) or read."),
					"jurisdiction": jurisdictionArg,
				}, "account_id", "bucket"),
			},
			{
				Name:         "revoke_r2_credential",
				OutputSchema: contract.OutputSchemaOf[CredentialRevoked](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "cloudflare.r2_credential", From: []string{"account_id", "access_key_id"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Revoke an R2 S3 key that create_r2_credential minted. It stops working at once. Refuses any token this plugin did not mint.",
				Examples: []string{
					"cerberus connectors exec cloudflare revoke_r2_credential --arg account_id=<account-id> --arg access_key_id=<access-key-id> --dry-run",
					"cerberus connectors exec cloudflare revoke_r2_credential --arg account_id=<account-id> --arg access_key_id=<access-key-id> --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"account_id":    contract.StringSchema("Cloudflare account ID."),
					"access_key_id": contract.StringSchema("The key's access key id, as create_r2_credential returned it."),
				}, "account_id", "access_key_id"),
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
