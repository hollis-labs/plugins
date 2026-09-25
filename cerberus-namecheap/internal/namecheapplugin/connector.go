package namecheapplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us.
//
// It is deliberately the id of the connector Cerberus used to compile in. The
// host resolves a plugin's secrets as <plugin id>/<secret name>, which is the
// key the built-in read, so CERBERUS_NAMECHEAP_API_KEY, a `namecheap:` entry
// in connector-secrets.yaml and keychain://namecheap/api_key all keep working
// with no migration. The same fact means the host refuses to install this
// plugin while the built-in is still registered: the id is reserved until the
// built-in is removed.
const ConnectorID = "namecheap"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.2.1"

// The manifest secret names, and the keys the host uses in the init config it
// hands us. They must stay what they are: existing credential references were
// written against them.
const (
	SecretAPIUser  = "api_user"
	SecretAPIKey   = "api_key"
	SecretUsername = "username"
	// SecretClientIP is the address Namecheap's API allow-list must contain.
	// The compiled-in connector read it but never declared it, and the host
	// hands a plugin only what its manifest declares, so it is declared here.
	// It is optional: without it the plugin sends 127.0.0.1, as the built-in
	// did, and Namecheap refuses the call unless that is allow-listed.
	SecretClientIP = "client_ip"
)

// DefaultClientIP is what the plugin sends when no client_ip is configured.
const DefaultClientIP = "127.0.0.1"

// ConfigSandbox selects Namecheap's sandbox API. It is a config field, not a
// secret: which endpoint to call is not a credential.
//
// Under the host it cannot be switched on yet. The host hands a plugin's Init
// only the secrets it resolved; declared config fields are not delivered, and
// there is no operator-facing place to set one. It takes effect through the
// host once that is built. A binary run directly honours SandboxEnvVar.
const ConfigSandbox = "sandbox"

// SandboxEnvVar selects the sandbox for a binary run directly, outside the
// host: "1" or "true".
const SandboxEnvVar = "CERBERUS_NAMECHEAP_SANDBOX"

// envVar is the variable the host resolves for a secret (CERBERUS_<ID>_<NAME>)
// and the one this binary reads when run directly, outside the host.
func envVar(secret string) string {
	return "CERBERUS_NAMECHEAP_" + upper(secret)
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

// recordsSchema is the one shape set_dns_record_set takes its records in.
var recordsSchema = map[string]any{"type": "array", "items": contract.ObjectSchema(map[string]any{
	"type":    contract.StringSchema("DNS type."),
	"host":    contract.StringSchema("Host name."),
	"value":   contract.StringSchema("Record value."),
	"ttl":     contract.IntegerSchema("TTL."),
	"mx_pref": contract.IntegerSchema("MX preference."),
}, "type", "host", "value")}

// Definition declares what this connector does. The operations, their input
// schemas and their effects match the compiled-in connector this plugin
// replaces, so a caller sees the same contract either side of the move.
//
// Two operations are deliberately absent: per-record create_dns_record and
// delete_dns_record. Namecheap has no per-record write; setHosts replaces the
// whole zone, and getHosts can omit records, so a "per-record" write computed
// from it silently deletes what it could not see. The host refuses an
// undeclared operation before calling the plugin, and the plugin refuses the
// two tool names itself as well (see refusedTools).
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Domain)},
		Capabilities:  contract.Capabilities{CanHealth: true},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{Name: "domain", Type: "string", Description: "Domain name in sld.tld form."},
				{Name: ConfigSandbox, Type: "boolean", Description: "Use the Namecheap sandbox API (api.sandbox.namecheap.com) instead of production. The sandbox needs its own account and key."},
			},
			// Only api_key is a credential. The user names and the allow-listed
			// address are kept with it and resolved the same way, but they are
			// names the plugin's own output shows ("the address sent is the
			// client_ip secret …"), so they are declared kind: name and the
			// host does not value-redact them.
			Secrets: []contract.SecretRequirement{
				{Name: SecretAPIUser, Description: "Namecheap API user.", Env: envVar(SecretAPIUser), Required: true, Kind: contract.SecretKindName},
				{Name: SecretAPIKey, Description: "Namecheap API key. Namecheap has no read-only scope: a key can change every domain in the account.", Env: envVar(SecretAPIKey), Required: true},
				{Name: SecretUsername, Description: "Namecheap username.", Env: envVar(SecretUsername), Required: true, Kind: contract.SecretKindName},
				{Name: SecretClientIP, Description: "The public address Namecheap's API allow-list contains for this account. Without it the plugin sends " + DefaultClientIP + ".", Env: envVar(SecretClientIP), Kind: contract.SecretKindName},
			},
		},
		// The contract matches the compiled-in connector's, except the
		// previews: here they are the plugin's claim, which the host cannot
		// verify. Finalize derives destructive, requires_ack and supports_dry.
		Operations: []contract.Operation{
			{
				Name:        "get_dns_record_set",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read visible host records and domain email_type. getHosts may omit records; this is not an authoritative zone backup.",
				InputSchema: contract.ObjectSchema(map[string]any{"domain": contract.StringSchema("Domain name.")}, "domain"),
				Examples:    []string{"cerberus connectors exec namecheap get_dns_record_set --arg domain=example.com"},
			},
			{
				Name: "set_dns_record_set",
				// Destructive, not write: every record the set omits is deleted.
				Effect:  contract.EffectDestructive,
				Target:  contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview: contract.PreviewPlugin,
				Output:  contract.OutputStructured,
				Cost:    contract.CostNone,
				LocalFS: contract.LocalFSNone,
				Description: "Replace ALL DNS hosts and explicitly set domain email routing. Supply an authoritative complete records array, including records getHosts hides. Omitted records are deleted. " +
					"The --dry-run preview reads the current records from Namecheap (a network read, so it needs credentials) and lists what would be added, removed and kept.",
				Examples: []string{
					`cerberus connectors exec namecheap set_dns_record_set --arg domain=example.com --arg email_type=MX --arg-json records='[{"type":"A","host":"@","value":"203.0.113.10","ttl":300}]' --dry-run --ack`,
					`cerberus connectors exec namecheap set_dns_record_set --input records.json --ack`,
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain":     contract.StringSchema("Domain name."),
					"email_type": map[string]any{"type": "string", "enum": []string{"MX", "MXE", "FWD", "OX", "NONE"}},
					"records":    recordsSchema,
				}, "domain", "email_type", "records"),
			},
			{
				Name:        "list_domains",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.account"},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List domains in the Namecheap account.",
				InputSchema: contract.ObjectSchema(map[string]any{}),
				Examples:    []string{"cerberus connectors exec namecheap list_domains"},
			},
			{
				Name:        "get_domain_status",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read detailed status for a Namecheap domain.",
				InputSchema: contract.ObjectSchema(map[string]any{"domain": contract.StringSchema("Domain name in sld.tld form.")}, "domain"),
				Examples:    []string{"cerberus connectors exec namecheap get_domain_status --arg domain=example.com"},
			},
			{
				Name:        "list_dns_records",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List DNS records for a Namecheap domain.",
				InputSchema: contract.ObjectSchema(map[string]any{"domain": contract.StringSchema("Domain name in sld.tld form.")}, "domain"),
				Examples:    []string{"cerberus connectors exec namecheap list_dns_records --arg domain=example.com"},
			},
			{
				Name:        "set_custom_nameservers",
				Effect:      contract.EffectWrite,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "namecheap.domain", From: []string{"domain"}},
				Preview:     contract.PreviewPlugin,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Switch a Namecheap domain to a custom nameserver set.",
				Examples: []string{
					"cerberus connectors exec namecheap set_custom_nameservers --arg domain=example.com --arg nameservers=ns1.example.net --arg nameservers=ns2.example.net --dry-run --ack",
					"cerberus connectors exec namecheap set_custom_nameservers --arg domain=example.com --arg nameservers=ns1.example.net --arg nameservers=ns2.example.net --ack",
				},
				InputSchema: contract.ObjectSchema(map[string]any{
					"domain": contract.StringSchema("Domain name in sld.tld form."),
					"nameservers": map[string]any{
						"type":        "array",
						"description": "List of nameservers to assign to the domain.",
						"items":       map[string]any{"type": "string"},
						"minItems":    2,
					},
				}, "domain", "nameservers"),
			},
		},
	})
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}
