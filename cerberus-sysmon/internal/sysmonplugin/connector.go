package sysmonplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "sysmon"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"sysmon.host"},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
		},
		Config: contract.ConfigSchema{
			Fields:  []contract.ConfigField{},
			Secrets: []contract.SecretRequirement{},
		},
		Operations: []contract.Operation{
			{
				Name:         "cpu",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get CPU usage metrics.",
				Examples:     []string{"cerberus connectors exec sysmon cpu"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "memory",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get memory usage metrics.",
				Examples:     []string{"cerberus connectors exec sysmon memory"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "disk",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get disk usage metrics.",
				Examples:     []string{"cerberus connectors exec sysmon disk --arg path=/"},
				InputSchema:  contract.ObjectSchema(map[string]any{
					"path": contract.StringSchema("Path to get disk usage for (default: /)."),
				}),
			},
			{
				Name:         "host",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get host OS information.",
				Examples:     []string{"cerberus connectors exec sysmon host"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
