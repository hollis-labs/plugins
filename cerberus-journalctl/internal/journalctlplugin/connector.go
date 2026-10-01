package journalctlplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "journalctl"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"journalctl.logs"},
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
				Name:         "logs",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "journalctl.logs"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Read logs for a specific systemd unit/service.",
				Examples:     []string{"cerberus connectors exec journalctl logs --arg unit=sshd --arg lines=50"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"unit":  contract.StringSchema("The systemd unit name (e.g. sshd)."),
					"lines": contract.IntegerSchema("Number of recent lines to fetch (default 100)."),
					"since": contract.StringSchema("Time threshold (e.g. '1 hour ago', 'today')."),
				}, "unit"),
			},
			{
				Name:         "system_logs",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "journalctl.logs"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Read global system logs.",
				Examples:     []string{"cerberus connectors exec journalctl system_logs --arg lines=50"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"lines": contract.IntegerSchema("Number of recent lines to fetch (default 100)."),
					"since": contract.StringSchema("Time threshold (e.g. '1 hour ago', 'today')."),
					"grep":  contract.StringSchema("Filter output by a specific string or regex."),
				}),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
