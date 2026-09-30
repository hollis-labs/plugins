package vercelplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// The host resolves a plugin's secrets as <plugin id>/<secret name>, and the
// deployment runner Cerberus used to compile in read vercel/token and
// vercel/scope. Keeping the id keeps CERBERUS_VERCEL_TOKEN, a `vercel:` entry
// in connector-secrets.yaml and keychain://vercel/token working with no
// migration.
const ConnectorID = "vercel"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Secret and field names. They are the names existing references were
// written against, so they must not change.
const (
	SecretToken = "token"
	SecretScope = "scope"
	// FieldProfilesFile names the operator-edited YAML file the deployment
	// profiles live in. It is a config field, so it comes from
	// connector-config.yaml and never from a caller.
	FieldProfilesFile = "profiles_file"
)

// The variables the host itself resolves for the secrets
// (CERBERUS_<ID>_<NAME>), and the ones this binary reads when run directly,
// outside the host.
const (
	TokenEnvVar = "CERBERUS_VERCEL_TOKEN" //nolint:gosec // a variable name, not a credential
	ScopeEnvVar = "CERBERUS_VERCEL_SCOPE"
)

// Operation names.
const (
	OpStatus       = "status"
	OpListProfiles = "list_profiles"
	OpDeploy       = "deploy"
)

// Definition declares what this connector does.
//
// deploy is exec: it runs the profile's preflight, build and deploy commands
// in a shell on this machine. What it runs is chosen by the operator's
// profiles file, never by the caller, who only names a profile. Its target is
// the profile, so a config.yaml resource with the same id labels it for
// policy; without one the target is unknown, which the host treats strictly.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"deploy_profile"},
		Capabilities:  contract.Capabilities{CanHealth: true},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{{
				Name: FieldProfilesFile, Type: "string",
				Description: "Path to the YAML file holding the deployment profiles, under a top-level profiles: list. A leading ~/ is the home directory.",
			}},
			Secrets: []contract.SecretRequirement{
				{
					Name:        SecretToken,
					Description: "Vercel access token, handed to the Vercel CLI as VERCEL_TOKEN. Optional: without one the CLI uses its own login session.",
					Env:         TokenEnvVar,
				},
				{
					Name:        SecretScope,
					Description: "Default Vercel team scope, used when a profile names none.",
					Env:         ScopeEnvVar,
					Kind:        contract.SecretKindName,
				},
			},
		},
		Operations: []contract.Operation{
			{
				Name:         OpStatus,
				OutputSchema: contract.OutputSchemaOf[Status](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "vercel.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSReads,
				Description:  "Report whether the Vercel CLI resolves, whether a token and a default scope are configured, and whether the profiles file parses. Makes no network call.",
				InputSchema:  contract.ObjectSchema(map[string]any{}),
				Examples:     []string{"cerberus connectors exec vercel status"},
			},
			{
				Name:         OpListProfiles,
				OutputSchema: contract.OutputSchemaOf[[]ProfileSummary](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "vercel.host"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSReads,
				Description:  "List the deployment profiles in the profiles file. Commands are not listed; a deploy's dry run shows them.",
				InputSchema:  contract.ObjectSchema(map[string]any{}),
				Examples:     []string{"cerberus connectors exec vercel list_profiles"},
			},
			{
				Name:         OpDeploy,
				OutputSchema: contract.OutputSchemaOf[RunResult](),
				Effect:       contract.EffectExec,
				Target:       contract.TargetDescriptor{Kind: "vercel.profile", From: []string{"profile"}},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSWrites,
				Description: "Run a deployment profile: its preflight and build commands, `vercel link` when the repo is not linked, then its deploy command, in the profile's repo_path. " +
					"The dry run returns the steps it would run, the profile's digest and the checkout's branch, commit and dirty state, and runs nothing.",
				Examples: []string{
					"cerberus connectors exec vercel deploy --arg profile=site --dry-run --ack",
					"cerberus connectors exec vercel deploy --arg profile=site --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"profile": contract.StringSchema("ID of a profile in the profiles file."),
				}, "profile"),
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
