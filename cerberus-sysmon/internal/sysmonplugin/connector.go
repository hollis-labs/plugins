package sysmonplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "sysmon"
const Version = "0.2.0"

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
				OutputSchema: contract.OutputSchemaOf[CPU](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "CPU model, logical cores, load averages and usage.",
				Examples:     []string{"cerberus connectors exec sysmon cpu"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "memory",
				OutputSchema: contract.OutputSchemaOf[Memory](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Memory and swap in bytes: total, free, available, used.",
				Examples:     []string{"cerberus connectors exec sysmon memory"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "disk",
				OutputSchema: contract.OutputSchemaOf[Disk](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Filesystem usage in bytes for the mount holding a path.",
				Examples:     []string{"cerberus connectors exec sysmon disk --arg path=/"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"path": contract.StringSchema("Path to get disk usage for (default: /)."),
				}),
			},
			{
				Name:         "host",
				OutputSchema: contract.OutputSchemaOf[Host](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Hostname, OS, architecture, kernel and uptime.",
				Examples:     []string{"cerberus connectors exec sysmon host"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "host_report",
				OutputSchema: contract.OutputSchemaOf[HostReport](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "sysmon.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "One report: host identity, CPU, memory, every real mount, GPUs (empty list when none) and warnings for sections that could not be read.",
				Examples:     []string{"cerberus connectors exec sysmon host_report"},
				InputSchema:  contract.ObjectSchema(map[string]any{}),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
