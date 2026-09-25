package azplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us. It must not collide with a built-in connector —
// `local`, `ssh`, `docker` and `github` are reserved and refused at install.
const ConnectorID = "azure"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Operation names, shared by the manifest and the tool router so the two cannot
// drift apart.
const (
	OpListSubscriptions    = "list_subscriptions"
	OpGetSubscription      = "get_subscription"
	OpListResourceGroups   = "list_resource_groups"
	OpListResources        = "list_resources"
	OpListAIAccounts       = "list_ai_accounts"
	OpListModelDeployments = "list_model_deployments"

	// ArgSubscriptionID is accepted by every operation, so a host with more
	// than one subscription does not need a config change to read the other.
	ArgSubscriptionID = "subscription_id"
	ArgResourceGroup  = "resource_group"
	ArgAccount        = "account"
)

// Definition declares what this connector does. The host derives the MCP tool
// names from it, so adding an operation here is the only registration step a
// plugin needs — unlike a built-in, which touches five files.
//
// Every operation is read-only: each declares effect read, with no preview.
// That is deliberate and is the whole scope of this connector. Cerberus does
// not own the Azure estate; an infrastructure team does. Lifecycle and write
// operations are documented as locked in README.md, with what would unlock
// them, rather than built speculatively.
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
				{
					Name:        ConfigSubscriptionID,
					Type:        "string",
					Description: "Default Azure subscription id. Omit when the account can see exactly one subscription; required when it can see several.",
				},
				{
					Name:        ConfigTenantID,
					Type:        "string",
					Description: "Entra tenant id for service principal auth. Omit to authenticate as the signed-in Azure CLI user.",
				},
				{
					Name:        ConfigClientID,
					Type:        "string",
					Description: "Application (client) id for service principal auth. Omit to authenticate as the signed-in Azure CLI user.",
				},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        SecretClientSecret,
				Description: "Service principal client secret. Optional: with no service principal configured the plugin authenticates as the signed-in Azure CLI user, which needs no stored credential.",
				Env:         ClientSecretEnvVar,
				Required:    false,
			}},
		},
		Operations: []contract.Operation{
			{
				Name:        OpListSubscriptions,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.tenant"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List the subscriptions this credential can see: id, display name, state, tenant.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors plugin managed exec azure list_subscriptions"},
			},
			{
				Name:        OpGetSubscription,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.subscription", From: []string{"subscription_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Report one subscription. With no argument it resolves the one operations act on by default, which is the fastest way to confirm which estate Cerberus is reading.",
				InputSchema: contract.ObjectSchema(map[string]any{
					ArgSubscriptionID: contract.StringSchema("Subscription id. Defaults to the configured subscription, or the only visible one."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec azure get_subscription"},
			},
			{
				Name:        OpListResourceGroups,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.subscription", From: []string{"subscription_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List resource groups in a subscription: name, location, provisioning state.",
				InputSchema: contract.ObjectSchema(map[string]any{
					ArgSubscriptionID: contract.StringSchema("Subscription id. Defaults to the configured subscription, or the only visible one."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec azure list_resource_groups"},
			},
			{
				Name:        OpListResources,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.subscription", From: []string{"subscription_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List the whole resource inventory of a subscription: type, name, resource group, location. Provider-specific properties are deliberately not returned — that field is an untyped blob.",
				InputSchema: contract.ObjectSchema(map[string]any{
					ArgSubscriptionID: contract.StringSchema("Subscription id. Defaults to the configured subscription, or the only visible one."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec azure list_resources"},
			},
			{
				Name:        OpListAIAccounts,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.subscription", From: []string{"subscription_id"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List Cognitive Services / AI Services accounts: name, kind, sku, endpoint. Keys are never returned; local_auth_disabled reports whether key auth is even enabled.",
				InputSchema: contract.ObjectSchema(map[string]any{
					ArgSubscriptionID: contract.StringSchema("Subscription id. Defaults to the configured subscription, or the only visible one."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec azure list_ai_accounts"},
			},
			{
				Name:        OpListModelDeployments,
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "azure.ai_account", From: []string{"subscription_id", "resource_group", "account"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List model deployments: deployment name, model, version, sku, capacity. Answers \"what models can we actually call, at what version\" without opening the portal. With no account it walks every AI account in the subscription.",
				InputSchema: contract.ObjectSchema(map[string]any{
					ArgSubscriptionID: contract.StringSchema("Subscription id. Defaults to the configured subscription, or the only visible one."),
					ArgAccount:        contract.StringSchema("Cognitive Services account name. Omit to walk every AI account in the subscription."),
					ArgResourceGroup:  contract.StringSchema("Resource group holding the account. Looked up from the account name when omitted."),
				}),
				Examples: []string{
					"cerberus connectors plugin managed exec azure list_model_deployments",
					"cerberus connectors plugin managed exec azure list_model_deployments --arg account=example-ai-account",
				},
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
