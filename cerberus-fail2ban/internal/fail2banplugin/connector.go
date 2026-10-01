package fail2banplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "fail2ban"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"fail2ban.jail"},
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
				Target:       contract.TargetDescriptor{Kind: "fail2ban.jail"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Get status of all jails or a specific jail.",
				Examples:     []string{"cerberus connectors exec fail2ban status --arg jail=sshd"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"jail": contract.StringSchema("The jail name (optional)."),
				}),
			},
			{
				Name:         "unban",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "fail2ban.jail"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Unban an IP from a jail.",
				Examples:     []string{"cerberus connectors exec fail2ban unban --arg jail=sshd --arg ip=1.2.3.4 --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"jail": contract.StringSchema("The jail name (e.g. sshd)."),
					"ip":   contract.StringSchema("The IP address to unban."),
				}, "jail", "ip"),
			},
			{
				Name:         "ban",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "fail2ban.jail"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Ban an IP in a jail.",
				Examples:     []string{"cerberus connectors exec fail2ban ban --arg jail=sshd --arg ip=1.2.3.4 --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"jail": contract.StringSchema("The jail name (e.g. sshd)."),
					"ip":   contract.StringSchema("The IP address to ban."),
				}, "jail", "ip"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
