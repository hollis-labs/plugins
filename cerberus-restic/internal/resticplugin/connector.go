package resticplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "restic"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"restic.repository"},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
		},
		Config: contract.ConfigSchema{
			Fields:  []contract.ConfigField{},
			Secrets: []contract.SecretRequirement{
				{
					Name:        "RESTIC_PASSWORD",
					Description: "Password for the restic repository", Env: "RESTIC_PASSWORD",
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:         "init",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Target:       contract.TargetDescriptor{Kind: "restic.repository"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Initialize a restic repository.",
				Examples:     []string{"cerberus connectors exec restic init --arg repo=~/dev/backups/restic-repo --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"repo": contract.StringSchema("The repository path or URL."),
				}, "repo"),
			},
			{
				Name:         "backup",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "restic.repository"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Backup a path to the repository.",
				Examples:     []string{"cerberus connectors exec restic backup --arg repo=~/dev/backups/restic-repo --arg path=/my/data --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"repo": contract.StringSchema("The repository path or URL."),
					"path": contract.StringSchema("The local path to backup."),
				}, "repo", "path"),
			},
			{
				Name:         "snapshots",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "restic.repository"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List snapshots in the repository.",
				Examples:     []string{"cerberus connectors exec restic snapshots --arg repo=~/dev/backups/restic-repo"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"repo": contract.StringSchema("The repository path or URL."),
				}, "repo"),
			},
			{
				Name:         "restore",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Target:       contract.TargetDescriptor{Kind: "restic.repository"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Restore a snapshot.",
				Examples:     []string{"cerberus connectors exec restic restore --arg repo=~/dev/backups/restic-repo --arg snapshot=latest --arg target=/restore/path --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"repo":     contract.StringSchema("The repository path or URL."),
					"snapshot": contract.StringSchema("Snapshot ID or 'latest'."),
					"target":   contract.StringSchema("Path to restore to."),
				}, "repo", "snapshot", "target"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
