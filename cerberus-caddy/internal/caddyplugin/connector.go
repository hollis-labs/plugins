package caddyplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
)

const ConnectorID = "caddy"
const Version = "0.2.0"

// SecretCloudflareToken is the declared secret holding the Cloudflare token
// Caddy uses for DNS-01 certificate issuance. It is a different token from the
// one the cloudflare plugin uses for DNS administration: scope it to
// Zone:DNS:Edit on the one zone, nothing else.
const SecretCloudflareToken = "cloudflare_api_token"

// SecretCloudflareTokenEnv is the variable the host itself resolves for the
// secret (never the plugin: the plugin reads only what arrives over Init).
const SecretCloudflareTokenEnv = "CADDY_CLOUDFLARE_DNS_TOKEN"

// routesSchema describes the declared routes.
func routesSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"minItems":    1,
		"description": "Declared routes. Each becomes <name>.<host>.<domain> (or <name>.<domain> without host) proxied to 127.0.0.1:<port>. From the CLI pass JSON text: --arg 'routes=[{\"name\":\"app\",\"host\":\"hub\",\"domain\":\"example.com\",\"port\":8080}]'.",
		"items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"name", "domain", "port"},
			"properties": map[string]any{
				"name":   contract.StringSchema("Service label, e.g. app."),
				"host":   contract.StringSchema("Optional host label(s) between name and domain, e.g. hub."),
				"domain": contract.StringSchema("Domain, e.g. example.com."),
				"port":   contract.IntegerSchema("Loopback port the service listens on (1-65535)."),
			},
		},
	}
}

// renderInputs is the property set render and apply share.
func renderInputs() map[string]any {
	return map[string]any{
		"routes": routesSchema(),
		"bind":   contract.StringSchema("The tailnet IP Caddy listens on (100.64.0.0/10 or fd7a:115c:a1e0::/48; loopback is accepted for testing). Rendered as default_bind. Wildcard and public addresses are refused."),
		"tls":    contract.StringSchema("TLS mode: cloudflare-dns (wildcard certificates by DNS-01, the default) or internal (Caddy's local CA)."),
		"email":  contract.StringSchema("Optional ACME account email."),
		"token_env": contract.StringSchema("Name of the environment variable the running caddy reads the DNS-01 token from, referenced as {env.NAME} (default " +
			DefaultTokenEnv + ")."),
		"config_path": contract.StringSchema("Caddyfile to compare with and, for apply, replace (default " + DefaultConfigPath + ")."),
	}
}

func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{"caddy.server"},
		Capabilities: contract.Capabilities{
			CanCreate:  false,
			CanDestroy: false,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{{
				Name:        ConfigCaddyPath,
				Type:        "string",
				Description: "Absolute path of the caddy binary. Optional: without it the plugin searches /usr/bin, /usr/local/bin, /opt/homebrew/bin, the snap paths, then PATH, on every call.",
			}},
			Secrets: []contract.SecretRequirement{{
				Name: SecretCloudflareToken,
				Description: "Cloudflare token for Caddy's DNS-01 challenge, scoped to Zone:DNS:Edit on one zone. " +
					"Used only to validate a candidate configuration; it is never written to the Caddyfile.",
				Env: SecretCloudflareTokenEnv,
			}},
		},
		Operations: []contract.Operation{
			{
				Name:         "reload",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "caddy.server"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Reloads the Caddy server configuration without downtime.",
				Examples:     []string{"cerberus connectors exec caddy reload --ack"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"config_path": contract.StringSchema("Path to the Caddyfile (optional)."),
				}),
			},
			{
				Name:         "validate",
				OutputSchema: contract.OutputSchemaOf[string](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "caddy.server"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSNone,
				Description:  "Validates a Caddyfile for syntax errors.",
				Examples:     []string{"cerberus connectors exec caddy validate"},
				InputSchema: contract.ObjectSchema(map[string]any{
					"config_path": contract.StringSchema("Path to the Caddyfile (optional)."),
				}),
			},
			{
				Name:         "render",
				OutputSchema: contract.OutputSchemaOf[RenderResult](),
				Effect:       contract.EffectRead,
				Target:       contract.TargetDescriptor{Kind: "caddy.server"},
				Preview:      contract.PreviewNone,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				// Reads config_path to diff against, like validate. Declared none
				// so a read stays a read under policy.
				LocalFS:     contract.LocalFSNone,
				Description: "Renders a Caddyfile from declared routes, bound only to the supplied address, and diffs it against the current file. Changes nothing.",
				Examples: []string{
					"cerberus connectors exec caddy render --arg bind=100.64.0.10 --arg 'routes=[{\"name\":\"app\",\"host\":\"hub\",\"domain\":\"example.com\",\"port\":8080}]'",
				},
				InputSchema: contract.ObjectSchema(renderInputs(), "routes", "bind"),
			},
			{
				Name:         "apply",
				OutputSchema: contract.OutputSchemaOf[ApplyResult](),
				Effect:       contract.EffectWrite,
				Reversible:   true,
				Target:       contract.TargetDescriptor{Kind: "caddy.server"},
				Preview:      contract.PreviewPlugin,
				Output:       contract.OutputStructured,
				Cost:         contract.CostNone,
				LocalFS:      contract.LocalFSWrites,
				Description:  "Renders the Caddyfile from declared routes, validates it, writes it and reloads Caddy; restores the previous file if validate or reload fails. --dry-run returns the diff only.",
				Examples: []string{
					"cerberus connectors exec caddy apply --arg bind=100.64.0.10 --arg 'routes=[{\"name\":\"app\",\"host\":\"hub\",\"domain\":\"example.com\",\"port\":8080}]' --dry-run",
					"cerberus connectors exec caddy apply --arg bind=100.64.0.10 --arg 'routes=[{\"name\":\"app\",\"host\":\"hub\",\"domain\":\"example.com\",\"port\":8080}]' --ack",
				},
				InputSchema: contract.ObjectSchema(renderInputs(), "routes", "bind"),
			},
		},
	})
}

func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
