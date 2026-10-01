package ufwplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "ufw"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"ufw.firewall"},
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
				Name:         "status",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "ufw.firewall"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Check UFW firewall status.",
				Examples:     []string{"cerberus connectors exec ufw status"},
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "allow",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "ufw.firewall"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Allow a port in UFW.",
				Examples:     []string{"cerberus connectors exec ufw allow --arg port=80/tcp --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"port": contract.StringSchema("Port to allow (e.g., 80/tcp)."),
				}, "port"),
			},
			{
				Name:         "deny",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "ufw.firewall"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Deny a port in UFW.",
				Examples:     []string{"cerberus connectors exec ufw deny --arg port=80/tcp --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"port": contract.StringSchema("Port to deny (e.g., 80/tcp)."),
				}, "port"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
