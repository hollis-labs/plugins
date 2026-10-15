package ghplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// It is deliberately the id of the connector Cerberus used to compile in. The
// host resolves a plugin's secrets as <plugin id>/<secret name>, which is the
// key the built-in read, so CERBERUS_GITHUB_TOKEN, a `github:` entry in
// connector-secrets.yaml and keychain://github/token all keep working with no
// migration. The same fact means the host refuses to install this plugin while
// the built-in is still registered: the id is reserved until the built-in is
// removed.
const ConnectorID = "github"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// SecretToken is the manifest secret name, and the key the host uses in the
// init config it hands us. It must stay "token": that is the name existing
// credential references were written against.
const SecretToken = "token"

// TokenEnvVar is the variable the host itself resolves for SecretToken
// (CERBERUS_<ID>_<NAME>), and the one this binary reads when run directly,
// outside the host.
const TokenEnvVar = "CERBERUS_GITHUB_TOKEN"

// defaultLimit and maxLimit bound the list operations. GitHub pages at 100.
const (
	defaultLimit = 10
	maxLimit     = 100
)

func repoProps(withLimit bool) map[string]any {
	props := map[string]any{
		"owner": contract.StringSchema("GitHub repository owner or organization."),
		"repo":  contract.StringSchema("GitHub repository name."),
	}
	if withLimit {
		props["limit"] = contract.IntegerSchema("Maximum number of records to return (1-100, default 10).")
	}
	return props
}

func readOperation(name, description string, outputSchema map[string]any, withLimit bool, example string) contract.Operation {
	return contract.Operation{
		Name:         name,
		OutputSchema: outputSchema,
		Effect:       contract.EffectRead,
		Target:       contract.TargetDescriptor{Kind: "github.repo", From: []string{"owner", "repo"}},
		Preview:      contract.PreviewNone,
		Output:       contract.OutputStructured,
		Cost:         contract.CostNone,
		LocalFS:      contract.LocalFSNone,
		Description:  description,
		InputSchema:  contract.ObjectSchema(repoProps(withLimit), "owner", "repo"),
		Examples:     []string{example},
	}
}

// Definition declares what this connector does. The operations, their input
// schemas, effects and output shapes match the compiled-in connector this
// plugin replaces, so a caller sees the same contract either side of the move.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Repo)},
		Capabilities:  contract.Capabilities{CanHealth: true},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "owner", Type: "string", Description: "GitHub repository owner or organization."},
				{Name: "repo", Type: "string", Description: "GitHub repository name."},
			},
			Secrets: []contract.SecretRequirement{{
				Name: SecretToken,
				Description: "GitHub API token. A fine-grained token with read-only Metadata, Contents and Actions " +
					"permissions on the repositories you query is enough for every operation.",
				Env:      TokenEnvVar,
				Required: true,
			}},
		},
		Operations: []contract.Operation{
			readOperation("status", "Read repository status: default branch, visibility and counts.",
				contract.OutputSchemaOf[RepoStatus](), false,
				"cerberus connectors exec github status --arg owner=hollis-labs --arg repo=cerberus"),
			readOperation("list_releases", "List recent repository releases, newest first.",
				contract.OutputSchemaOf[[]Release](), true,
				"cerberus connectors exec github list_releases --arg owner=hollis-labs --arg repo=cerberus --arg limit=5"),
			readOperation("list_workflow_runs", "List recent GitHub Actions workflow runs, newest first.",
				contract.OutputSchemaOf[[]WorkflowRun](), true,
				"cerberus connectors exec github list_workflow_runs --arg owner=hollis-labs --arg repo=cerberus --arg limit=5"),
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
