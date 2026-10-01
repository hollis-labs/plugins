package aptplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "apt"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"apt.package"},
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
				Name:         "update",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "apt.package"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Update the local package index.",
				Examples:     []string{"cerberus connectors exec apt update --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "upgrade",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "apt.package"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Upgrade installed packages.",
				Examples:     []string{"cerberus connectors exec apt upgrade --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "install",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "apt.package"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Install an APT package.",
				Examples:     []string{"cerberus connectors exec apt install --arg package=curl --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"package": contract.StringSchema("Package to install."),
				}, "package"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
