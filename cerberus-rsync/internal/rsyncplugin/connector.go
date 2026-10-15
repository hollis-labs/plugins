package rsyncplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "rsync"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"rsync.job"},
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
				Name:         "sync",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "rsync.job"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Sync a source path to a destination path.",
				Examples:     []string{"cerberus connectors exec rsync sync --arg source=/var/www --arg dest=~/dev/backups/www --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"source": contract.StringSchema("The source path."),
					"dest":   contract.StringSchema("The destination path."),
					"delete": map[string]any{"type": "boolean", "description": "Delete extraneous files from dest directories (default false)."},
				}, "source", "dest"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
