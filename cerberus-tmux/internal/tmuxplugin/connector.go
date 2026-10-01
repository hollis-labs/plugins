package tmuxplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "tmux"
const Version = "0.1.0"

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"tmux.session"},
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
				Name:         "list",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "tmux.session"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "List all tmux sessions.",
				Examples:     []string{"cerberus connectors exec tmux list"},
				InputSchema: contract.ObjectSchema(map[string]any{}),
			},
			{
				Name:         "new",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "tmux.session"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Create a new detached tmux session running a command.",
				Examples:     []string{"cerberus connectors exec tmux new --arg session_name=build --arg command=\"make build\""},
				InputSchema: contract.ObjectSchema(map[string]any{
					"session_name": contract.StringSchema("Name of the tmux session."),
					"command":      contract.StringSchema("Command to run inside the session (optional)."),
				}, "session_name"),
			},
			{
				Name:         "capture",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "tmux.session"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Capture the output of a tmux pane.",
				Examples:     []string{"cerberus connectors exec tmux capture --arg session_name=build"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"session_name": contract.StringSchema("Name of the tmux session."),
				}, "session_name"),
			},
			{
				Name:         "kill",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectDestructive,
				Target:       contract.TargetDescriptor{Kind: "tmux.session"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Kill a tmux session.",
				Examples:     []string{"cerberus connectors exec tmux kill --arg session_name=build --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"session_name": contract.StringSchema("Name of the tmux session."),
				}, "session_name"),
			},
			{
				Name:         "send_keys",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "tmux.session"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Send keys to a tmux session.",
				Examples:     []string{"cerberus connectors exec tmux send_keys --arg session_name=build --arg keys=\"C-c\""},
				InputSchema: contract.ObjectSchema(map[string]any{
					"session_name": contract.StringSchema("Name of the tmux session."),
					"keys":         contract.StringSchema("Keys to send (e.g., C-c, Enter, text)."),
				}, "session_name", "keys"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
