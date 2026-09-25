package cfplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us. It must not collide with a built-in connector.
const ConnectorID = "contextforge"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Definition declares what this connector does. The host derives the MCP tool
// names from it, so adding an operation here is the only registration step a
// plugin needs — unlike a built-in, which touches five files.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanHealth: true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{{
				Name:        "address",
				Type:        "string",
				Description: "ContextForge base URL. Defaults to the tunnel-host-a local endpoint.",
				Default:     DefaultAddress,
			}},
			Secrets: []contract.SecretRequirement{{
				Name:        "token",
				Description: "ContextForge admin JWT. Bearer only — X-API-Key and a raw token both 401.",
				Env:         TokenEnvVar,
				Required:    true,
			}},
		},
		Operations: []contract.Operation{
			{
				Name:        "get_health",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "contextforge.gateway"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Report ContextForge reachability. /health is open, so this works without a token and is the fastest way to tell a down tunnel from a down gateway.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec contextforge get_health"},
			},
			{
				Name:        "list_gateways",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "contextforge.gateway"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List upstream MCP server registrations. Credentials are never returned: auth_type reports the kind of auth configured, auth_configured whether any is set.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec contextforge list_gateways"},
			},
			{
				Name:        "list_virtual_servers",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "contextforge.gateway"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List the composed catalogs. These have no auth fields; auth lives on the gateway.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec contextforge list_virtual_servers"},
			},
			{
				Name:        "list_tools",
				Effect:      contract.EffectReadSensitive,
				Target:      contract.TargetDescriptor{Kind: "contextforge.gateway"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputFreeText,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List tool registrations. Names are gateway-prefixed and change when a virtual server is renamed, so read them here rather than guessing.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec contextforge list_tools"},
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
