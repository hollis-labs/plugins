package digitaloceanplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// It is deliberately the id of the connector Cerberus used to compile in. The
// host resolves a plugin's secrets as <plugin id>/<secret name>, which is the
// key the built-in read, so CERBERUS_DIGITALOCEAN_API_TOKEN, a `digitalocean:`
// entry in connector-secrets.yaml and keychain://digitalocean/api_token all
// keep working with no migration. The same fact means the host refuses to
// install this plugin while the built-in is still registered: the id is
// reserved until the built-in is removed.
const ConnectorID = "digitalocean"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.2.0"

// SecretAPIToken is the manifest secret name, and the key the host uses in the
// init config it hands us. It must stay "api_token": that is the name existing
// credential references were written against.
const SecretAPIToken = "api_token"

// TokenEnvVar is the variable the host itself resolves for SecretAPIToken
// (CERBERUS_<ID>_<NAME>), and the one this binary reads when run directly,
// outside the host.
const TokenEnvVar = "CERBERUS_DIGITALOCEAN_API_TOKEN"

// Definition declares what this connector does. The operations, their input
// schemas and their effects match the compiled-in connector this plugin
// replaces, so a caller sees the same contract either side of the move.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanCreate:  true,
			CanDestroy: true,
			CanHealth:  true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "droplet_id", Type: "integer", Description: "DigitalOcean droplet ID."},
				{Name: "name", Type: "string", Description: "Droplet name."},
				{Name: "region", Type: "string", Description: "Droplet region slug."},
				{Name: "size", Type: "string", Description: "Droplet size slug."},
				{Name: "image", Type: "string", Description: "Droplet image slug.", Required: true},
				{Name: "ssh_keys", Type: "array", Description: "SSH key fingerprints."},
				{Name: "user_data", Type: "string", Description: "Cloud-init user-data."},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        SecretAPIToken,
				Description: "DigitalOcean API token. A custom-scoped token with droplet:read is enough for the read operations.",
				Env:         TokenEnvVar,
				Required:    true,
			}},
		},
		// The contract matches the compiled-in connector's, except the
		// preview: here it is the plugin's claim, which the host cannot verify.
		// Finalize derives destructive, requires_ack and supports_dry from it.
		Operations: []contract.Operation{
			{
				Name:        "list_droplets",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.account"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List DigitalOcean droplets.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec digitalocean list_droplets"},
			},
			{
				Name:        "get_droplet",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.droplet", From: []string{"droplet_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Get detailed status for a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
				Examples: []string{"cerberus connectors exec digitalocean get_droplet --arg droplet_id=123456"},
			},
			{
				Name:        "create_droplet",
				Effect:      contract.EffectWrite,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.account"},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputStructured,
				Cost:        contract.CostBillable,
				LocalFS:     contract.LocalFSNone,
				Description: "Create a new DigitalOcean droplet. It is billable and runs the supplied cloud-init user_data as root.",
				Examples: []string{
					"cerberus connectors exec digitalocean create_droplet --arg name=web-1 --arg region=nyc3 --arg size=s-1vcpu-1gb --arg image=ubuntu-24-04-x64 --dry-run",
					"cerberus connectors exec digitalocean create_droplet --arg name=web-1 --arg region=nyc3 --arg size=s-1vcpu-1gb --arg image=ubuntu-24-04-x64 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"name":      contract.StringSchema("Droplet name."),
					"region":    contract.StringSchema("Droplet region slug."),
					"size":      contract.StringSchema("Droplet size slug."),
					"image":     contract.StringSchema("Droplet image slug."),
					"ssh_keys":  map[string]any{"type": "array", "description": "SSH key fingerprints.", "items": map[string]any{"type": "string"}},
					"user_data": contract.StringSchema("Cloud-init user-data."),
				}, "name", "region", "size", "image"),
			},
			{
				Name:        "start",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.droplet", From: []string{"droplet_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Power on a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
				Examples: []string{"cerberus connectors exec digitalocean start --arg droplet_id=123456"},
			},
			{
				Name:        "stop",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.droplet", From: []string{"droplet_id"}},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Power off a droplet. A hard power-off takes down whatever it serves.",
				Examples: []string{
					"cerberus connectors exec digitalocean stop --arg droplet_id=123456 --dry-run",
					"cerberus connectors exec digitalocean stop --arg droplet_id=123456 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
			},
			{
				Name:        "destroy",
				Effect:      contract.EffectDestructive,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.droplet", From: []string{"droplet_id"}},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Destroy a droplet permanently.",
				Examples: []string{
					"cerberus connectors exec digitalocean destroy --arg droplet_id=123456 --dry-run",
					"cerberus connectors exec digitalocean destroy --arg droplet_id=123456 --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
			},
			{
				Name:        "status",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "digitalocean.droplet", From: []string{"droplet_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read normalized runtime state for a droplet.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"droplet_id": contract.IntegerSchema("DigitalOcean droplet ID."),
				}, "droplet_id"),
				Examples: []string{"cerberus connectors exec digitalocean status --arg droplet_id=123456"},
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
