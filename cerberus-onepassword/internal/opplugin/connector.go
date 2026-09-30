package opplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us. The host resolves this plugin's own credential
// as onepassword/<secret name>.
const ConnectorID = "onepassword"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Scheme is the secret reference scheme this plugin resolves: 1Password's
// own, op://<vault>/<item>/[<section>/]<field>.
const Scheme = "op"

// SecretServiceAccountToken is the one credential this plugin declares: a
// 1Password service account token (ops_…).
//
// It is the key to every vault the service account can read, so it comes
// only from the OS credential store (or the environment), never from another
// vault: the host resolves a secret backend's own credential through its core
// chain alone.
const SecretServiceAccountToken = "service_account_token"

// ResolveCommand is the plugin-sdk command/execute name the host sends to
// resolve one reference. It is not a connector operation, so it is never a
// CLI command, an API operation or an MCP tool: the host reaches it only from
// its own secret resolution.
const ResolveCommand = cerbplugin.ResolveCommand

// envVar is the variable the host resolves for a secret (CERBERUS_<ID>_<NAME>)
// and the one this binary reads when run directly, outside the host.
func envVar(secret string) string {
	return "CERBERUS_ONEPASSWORD_" + upper(secret)
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
		// 1Password is a remote service this plugin reads from, which is the
		// sense contextforge's gateway uses server in.
		ResourceTypes: []string{string(resource.Server)},
		Capabilities:  contract.Capabilities{CanHealth: true},
		Config: contract.ConfigSchema{
			Secrets: []contract.SecretRequirement{
				{
					Name: SecretServiceAccountToken,
					Description: "1Password service account token (ops_…). Create a service account with read access to only the vaults Cerberus may read, " +
						"and store its token in the OS credential store.",
					Env:      envVar(SecretServiceAccountToken),
					Required: true,
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:         "status",
				OutputSchema: contract.OutputSchemaOf[Status](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "onepassword.account"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description: "Report whether a service account token arrived and which 1Password sign-in address it names. " +
					"Makes no network call and reads no item.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec onepassword status"},
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}

// Status is the status operation's result. It carries no item data and no
// part of the token beyond the sign-in address it names.
type Status struct {
	Configured    bool   `json:"configured"`
	SignInAddress string `json:"sign_in_address,omitempty"`
	Scheme        string `json:"scheme"`
	Message       string `json:"message"`
}
