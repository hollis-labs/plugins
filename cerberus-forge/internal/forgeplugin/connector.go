package forgeplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// It is deliberately the id of the connector Cerberus used to compile in. The
// host resolves a plugin's secrets as <plugin id>/<secret name>, which is the
// key the built-in read, so CERBERUS_FORGE_API_TOKEN, a `forge:` entry in
// connector-secrets.yaml and keychain://forge/api_token all keep working with
// no migration. The same fact means the host refuses to install this plugin
// while the built-in is still registered: the id is reserved until the
// built-in is removed.
const ConnectorID = "forge"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// SecretAPIToken is the manifest secret name, and the key the host uses in the
// init config it hands us. It must stay "api_token": that is the name existing
// credential references were written against.
const SecretAPIToken = "api_token"

// TokenEnvVar is the variable the host itself resolves for SecretAPIToken
// (CERBERUS_<ID>_<NAME>), and the one this binary reads when run directly,
// outside the host.
const TokenEnvVar = "CERBERUS_FORGE_API_TOKEN"

func serverSiteSchema(extra map[string]any) map[string]any {
	props := map[string]any{
		"server_id": contract.IntegerSchema("Forge server ID."),
		"site_id":   contract.IntegerSchema("Forge site ID."),
	}
	for key, value := range extra {
		props[key] = value
	}
	return props
}

// Definition declares what this connector does. The operations, their input
// schemas and their effects match the compiled-in connector this plugin
// replaces, so a caller sees the same contract either side of the move.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanHealth: true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "server_id", Type: "integer", Description: "Forge server ID."},
				{Name: "site_id", Type: "integer", Description: "Forge site ID."},
				{Name: "command", Type: "string", Description: "Command to execute within the site's root directory."},
				{Name: "content", Type: "string", Description: "Deployment script content."},
				{Name: "auto_source", Type: "boolean", Description: "Automatically source environment variables in the deploy script."},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        SecretAPIToken,
				Description: "Laravel Forge API token. Forge tokens are account-wide; there is no read-only scope.",
				Env:         TokenEnvVar,
				Required:    true,
			}},
		},
		// The contract matches the compiled-in connector's, except the
		// previews: here they are the plugin's claim, which the host cannot
		// verify, and update_deployment_script gains one (a diff against the
		// current script). Finalize derives destructive, requires_ack and
		// supports_dry from it.
		Operations: []contract.Operation{
			{
				Name:        "list_servers",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "forge.account"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List Forge servers.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec forge list_servers"},
			},
			{
				Name:        "get_server",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "forge.server", From: []string{"server_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Get a Forge server.",
				InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID.")}, "server_id"),
				Examples:    []string{"cerberus connectors exec forge get_server --arg server_id=12"},
			},
			{
				Name:        "list_sites",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "forge.server", From: []string{"server_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List sites on a Forge server.",
				InputSchema: contract.ObjectSchema(map[string]any{"server_id": contract.IntegerSchema("Forge server ID.")}, "server_id"),
				Examples:    []string{"cerberus connectors exec forge list_sites --arg server_id=12"},
			},
			{
				Name:        "get_deployment_script",
				Effect:      contract.EffectReadSensitive,
				Target:      contract.TargetDescriptor{Kind: "forge.site", From: []string{"server_id", "site_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputFreeText,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read a site's deployment script. A script can carry secrets, so this is read_sensitive.",
				InputSchema: contract.ObjectSchema(serverSiteSchema(nil), "server_id", "site_id"),
				Examples:    []string{"cerberus connectors exec forge get_deployment_script --arg server_id=12 --arg site_id=34"},
			},
			{
				Name:       "update_deployment_script",
				Effect:     contract.EffectWrite,
				Reversible: true,
				Target:     contract.TargetDescriptor{Kind: "forge.site", From: []string{"server_id", "site_id"}},
				Preview:    contract.PreviewPlugin,
				Output:     contract.OutputStructured,
				Cost:       contract.CostNone,
				LocalFS:    contract.LocalFSNone,
				Description: "Update a site's deployment script. The script is what deploy_site runs next. " +
					"The dry run reads the current script (a real Forge call, so it needs the token) and returns a line diff against the proposed content; " +
					"the diff shows changed script lines, which can carry secrets.",
				Examples: []string{
					"cerberus connectors exec forge update_deployment_script --arg server_id=12 --arg site_id=34 --arg content=\"$(cat deploy.sh)\" --dry-run --ack",
					"cerberus connectors exec forge update_deployment_script --arg server_id=12 --arg site_id=34 --arg content=\"$(cat deploy.sh)\" --ack",
				},
				InputSchema: contract.ObjectSchema(serverSiteSchema(map[string]any{
					"content":     contract.StringSchema("Deployment script content."),
					"auto_source": map[string]any{"type": "boolean", "description": "Automatically source environment variables."},
				}), "server_id", "site_id", "content"),
			},
			{
				Name:        "deploy_site",
				Effect:      contract.EffectLifecycle,
				Target:      contract.TargetDescriptor{Kind: "forge.site", From: []string{"server_id", "site_id"}},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Trigger a Forge site deployment.",
				Examples: []string{
					"cerberus connectors exec forge deploy_site --arg server_id=12 --arg site_id=34 --dry-run --ack",
					"cerberus connectors exec forge deploy_site --arg server_id=12 --arg site_id=34 --ack",
				},
				InputSchema: contract.ObjectSchema(serverSiteSchema(nil), "server_id", "site_id"),
			},
			{
				Name:        "exec_site_command",
				Effect:      contract.EffectExec,
				Target:      contract.TargetDescriptor{Kind: "forge.site", From: []string{"server_id", "site_id"}},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputFreeText,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Execute a command on a Forge site.",
				Examples: []string{
					"cerberus connectors exec forge exec_site_command --arg server_id=12 --arg site_id=34 --arg command='php artisan migrate --force' --dry-run --ack",
					"cerberus connectors exec forge exec_site_command --arg server_id=12 --arg site_id=34 --arg command='php artisan migrate --force' --ack",
				},
				InputSchema: contract.ObjectSchema(serverSiteSchema(map[string]any{
					"command": contract.StringSchema("Command to execute."),
				}), "server_id", "site_id", "command"),
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
