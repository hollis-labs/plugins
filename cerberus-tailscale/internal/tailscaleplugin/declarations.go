package tailscaleplugin

import (
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "tailscale"
const Version = "0.2.0"

// The declared secrets. The host resolves each as <plugin id>/<name> and hands
// the values over the Init channel; none travels in the environment. Either an
// API key, or an OAuth client (id and secret together), is enough.
const (
	SecretAPIKey            = "api_key"
	SecretOAuthClientID     = "oauth_client_id"
	SecretOAuthClientSecret = "oauth_client_secret"

	// Env names the host itself resolves for each secret (CERBERUS_<ID>_<NAME>),
	// and the ones this binary reads when run directly, outside the host.
	APIKeyEnvVar            = "CERBERUS_TAILSCALE_API_KEY"
	OAuthClientIDEnvVar     = "CERBERUS_TAILSCALE_OAUTH_CLIENT_ID"
	OAuthClientSecretEnvVar = "CERBERUS_TAILSCALE_OAUTH_CLIENT_SECRET"
)

// ConfigTailnet is the config field naming the tailnet, and DefaultTailnet what
// the API calls "the credential's own tailnet".
const (
	ConfigTailnet  = "tailnet"
	DefaultTailnet = "-"
)

const mintNote = "Store this key now. Tailscale shows its value only once and it cannot be read again."

// Tailscale's own ceiling on an auth key's lifetime is 90 days.
const (
	defaultMintTTL = time.Hour
	minMintTTL     = time.Minute
	maxMintTTL     = 90 * 24 * time.Hour
)

var (
	tailnetTarget = contract.TargetDescriptor{Kind: "tailscale.tailnet"}
	deviceTarget  = contract.TargetDescriptor{Kind: "tailscale.device", From: []string{"device_id"}}
	tailnetArg    = contract.StringSchema("Tailnet to act on, overriding the configured one. Default: the configured tailnet.")
	deviceIDArg   = contract.StringSchema("Device id (nodeId or numeric id), as list_devices shows it.")
	tagsArg       = func(desc string) map[string]any {
		return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}
	}
	boolArg = func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
)

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"tailscale.tailnet"},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: ConfigTailnet, Type: "string", Description: "Tailnet the API operations act on: its name (for example example.com) or id. The default, a hyphen, is the tailnet the credential belongs to."},
			},
			// None is Required: either an API key or an OAuth client pair is
			// enough, so the host cannot know which one is missing. The plugin
			// loads without them, API calls fail as credential_missing, and the
			// local-CLI operations and Health keep working.
			Secrets: []contract.SecretRequirement{
				{Name: SecretAPIKey, Description: "Tailscale API access token. Alternative to an OAuth client; it acts as the user who created it and expires within 90 days.", Env: APIKeyEnvVar},
				{Name: SecretOAuthClientID, Description: "Tailscale OAuth client id. Used with the client secret; preferred to an API key.", Env: OAuthClientIDEnvVar},
				{Name: SecretOAuthClientSecret, Description: "Tailscale OAuth client secret.", Env: OAuthClientSecretEnvVar},
			},
		},
		Operations: []contract.Operation{
			{
				Name:         "status",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "tailscale.tailnet"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Check Tailscale connection status.",
				Examples:     []string{"cerberus connectors exec tailscale status"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "up",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "tailscale.tailnet"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Bring the tailnet connection up.",
				Examples:     []string{"cerberus connectors exec tailscale up --ack"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "down",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "tailscale.tailnet"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Bring the tailnet connection down.",
				Examples:     []string{"cerberus connectors exec tailscale down --ack"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "serve",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "tailscale.tailnet"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Expose a local port to the tailnet or internet (funnel).",
				Examples:     []string{"cerberus connectors exec tailscale serve --arg port=8080 --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"port":   contract.StringSchema("Port to serve (e.g., 8080)."),
					"funnel": map[string]any{"type": "boolean", "description": "Whether to expose to the public internet using funnel."},
				}, "port"),
			},

			// --- control-plane API operations ---
			{
				Name:         "list_auth_keys",
				OutputSchema: contract.OutputSchemaOf[AuthKeyList](),
				Effect:       contract.EffectRead,
				Target:       tailnetTarget,
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List the tailnet's keys (ids, kind, expiry, capabilities). Key values are never returned.",
				Examples:     []string{"cerberus connectors exec tailscale list_auth_keys"},
				InputSchema:  contract.ObjectSchema(map[string]any{"tailnet": tailnetArg}),
			},
			{
				Name:         "mint_auth_key",
				OutputSchema: contract.OutputSchemaOf[MintedKey](),
				Effect:       contract.EffectDestructive,
				Target:       tailnetTarget,
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Mint a tailnet auth key, a credential that lets a machine join the tailnet. Ephemeral, single-use and pre-authorized by default, tagged, one hour. The key value is returned once and cannot be read again.",
				Examples: []string{
					"cerberus connectors exec tailscale mint_auth_key --arg tags=tag:ci --dry-run",
					"cerberus connectors exec tailscale mint_auth_key --arg tags=tag:ci --arg description=ci-runner --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"tags":          tagsArg("Tags the joining device gets (each tag:name). Required: a tagged device is not tied to a person."),
					"description":   contract.StringSchema("What the key is for (50 characters at most)."),
					"reusable":      boolArg("Allow more than one device to join with the key. Default false (single use)."),
					"ephemeral":     boolArg("Devices that join are removed when they go offline. Default true."),
					"preauthorized": boolArg("Devices join without manual approval. Default true."),
					"ttl_seconds":   contract.IntegerSchema("Key lifetime in seconds, 60 to 7776000 (90 days). Default 3600."),
					"tailnet":       tailnetArg,
				}, "tags"),
			},
			{
				Name:         "revoke_key",
				OutputSchema: contract.OutputSchemaOf[KeyAction](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "tailscale.key", From: []string{"key_id"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Revoke a key. It stops working at once; devices that already joined with it stay.",
				Examples: []string{
					"cerberus connectors exec tailscale revoke_key --arg key_id=kExample123 --dry-run",
					"cerberus connectors exec tailscale revoke_key --arg key_id=kExample123 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"key_id":  contract.StringSchema("Key id, as list_auth_keys shows it."),
					"tailnet": tailnetArg,
				}, "key_id"),
			},
			{
				Name:         "list_devices",
				OutputSchema: contract.OutputSchemaOf[DeviceList](),
				Effect:       contract.EffectRead,
				Target:       tailnetTarget,
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List the tailnet's devices.",
				Examples:     []string{"cerberus connectors exec tailscale list_devices"},
				InputSchema:  contract.ObjectSchema(map[string]any{"tailnet": tailnetArg}),
			},
			{
				Name:         "get_device",
				OutputSchema: contract.OutputSchemaOf[Device](),
				Effect:       contract.EffectRead,
				Target:       deviceTarget,
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get one device.",
				Examples:     []string{"cerberus connectors exec tailscale get_device --arg device_id=nExample123"},
				InputSchema:  contract.ObjectSchema(map[string]any{"device_id": deviceIDArg}, "device_id"),
			},
			{
				Name:         "delete_device",
				OutputSchema: contract.OutputSchemaOf[DeviceAction](),
				Effect:       contract.EffectDestructive,
				Target:       deviceTarget,
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Remove a device from the tailnet. It must rejoin to come back, and a tagged or ephemeral node may need a new key.",
				Examples: []string{
					"cerberus connectors exec tailscale delete_device --arg device_id=nExample123 --dry-run",
					"cerberus connectors exec tailscale delete_device --arg device_id=nExample123 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{"device_id": deviceIDArg}, "device_id"),
			},
			{
				Name:         "set_tags",
				OutputSchema: contract.OutputSchemaOf[DeviceAction](),
				Effect:       contract.EffectDestructive,
				Reversible:   true,
				Target:       deviceTarget,
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Replace a device's tags. Tags decide which ACL rules apply to it, so this can widen or cut its access. An empty list clears them.",
				Examples: []string{
					"cerberus connectors exec tailscale set_tags --arg device_id=nExample123 --arg tags=tag:server --dry-run",
					"cerberus connectors exec tailscale set_tags --arg device_id=nExample123 --arg tags=tag:server --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"device_id": deviceIDArg,
					"tags":      tagsArg("The complete new tag set (each tag:name). Empty clears."),
				}, "device_id"),
			},
			{
				Name:         "authorize_device",
				OutputSchema: contract.OutputSchemaOf[DeviceAction](),
				Effect:       contract.EffectDestructive,
				Reversible:   true,
				Target:       deviceTarget,
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Authorize a device that is waiting for approval, which admits it to the tailnet.",
				Examples: []string{
					"cerberus connectors exec tailscale authorize_device --arg device_id=nExample123 --dry-run",
					"cerberus connectors exec tailscale authorize_device --arg device_id=nExample123 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{"device_id": deviceIDArg}, "device_id"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
