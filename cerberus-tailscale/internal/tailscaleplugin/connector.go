package tailscaleplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "tailscale"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"tailscale.tailnet"},
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
					"port": contract.StringSchema("Port to serve (e.g., 8080)."),
					"funnel": map[string]any{"type": "boolean", "description": "Whether to expose to the public internet using funnel."},
				}, "port"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
