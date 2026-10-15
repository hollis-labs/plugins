package keeperplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us. The host resolves this plugin's own credential
// as keeper/<secret name>.
const ConnectorID = "keeper"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Scheme is the secret reference scheme this plugin resolves: Keeper's own
// notation, keeper://<record uid or title>/<selector>/<name>.
const Scheme = "keeper"

// SecretKSMConfig is the one credential this plugin declares: a bound Keeper
// Secrets Manager client configuration, base64 or JSON, as Keeper's own
// `ksm init default <one-time token>` prints it.
//
// It is the key to every record shared with the Secrets Manager application,
// so it comes only from the OS credential store (or the environment), never
// from another vault: the host resolves a secret backend's own credential
// through its core chain alone.
const SecretKSMConfig = "ksm_config"

// ResolveCommand is the plugin-sdk command/execute name the host sends to
// resolve one reference. It is not a connector operation, so it is never a
// CLI command, an API operation or an MCP tool: the host reaches it only from
// its own secret resolution.
const ResolveCommand = cerbplugin.ResolveCommand

// envVar is the variable the host resolves for a secret (CERBERUS_<ID>_<NAME>)
// and the one this binary reads when run directly, outside the host.
func envVar(secret string) string {
	return "CERBERUS_KEEPER_" + upper(secret)
}

func upper(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 'a' + 'A'
		}
	}
	return string(out)
}

// Definition declares what this connector does as a connector: one offline
// status read. Resolving a secret is deliberately not an operation (see
// ResolveCommand).
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:      ConnectorID,
		Version: Version,
		// The manifest requires a resource type and none names a vault.
		// Keeper is a remote service this plugin reads from, which is the
		// sense contextforge's gateway uses server in.
		ResourceTypes: []string{string(resource.Server)},
		Capabilities:  contract.Capabilities{CanHealth: true},
		Config: contract.ConfigSchema{
			Secrets: []contract.SecretRequirement{
				{
					Name: SecretKSMConfig,
					Description: "Bound Keeper Secrets Manager client configuration (base64 or JSON). Bind the one-time access token with Keeper's own tooling, " +
						"outside Cerberus, and store the configuration it prints in the OS credential store.",
					Env:      envVar(SecretKSMConfig),
					Required: true,
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:         "status",
				OutputSchema: contract.OutputSchemaOf[Status](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "keeper.application"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description: "Report whether a bound Keeper Secrets Manager configuration arrived and which Keeper region it targets. " +
					"Makes no network call and reads no record.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec keeper status"},
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}

// Status is the status operation's result. It carries no record data and no
// part of the configuration beyond the region's hostname.
type Status struct {
	Configured bool   `json:"configured"`
	Hostname   string `json:"hostname,omitempty"`
	Scheme     string `json:"scheme"`
	Message    string `json:"message"`
}
