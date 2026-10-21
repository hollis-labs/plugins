package pgplugin

import contract "github.com/hollis-labs/cerberus/pkg/connector"

const ConnectorID = "postgres"
const Version = "0.3.0"

func Definition() contract.Definition {
	fields := []contract.ConfigField{}
	for _, f := range []struct{ name, description string }{
		{"cluster", "Operator-defined cluster identity; calls must match it."},
		{"host", "PostgreSQL DNS hostname (TLS verify-full), or absolute Unix socket directory."},
		{"port", "PostgreSQL port, 1..65535."},
		{"user", "Catalog reader username. No password or DSN in this field."},
		{"admin_user", "Dedicated write-side superuser; admin_password must be bound only to the write process."},
		{"database", "Maintenance database for catalog reads."},
		{"ca_file", "Optional PEM trust roots for TLS; system trust otherwise."},
	} {
		fields = append(fields, contract.ConfigField{Name: f.name, Type: "string", Description: f.description})
	}
	common := map[string]any{"cluster": contract.StringSchema("Configured cluster identity; no ad-hoc endpoints.")}
	plan := map[string]any{"cluster": common["cluster"], "app": contract.StringSchema("Lowercase app slug, max 20 characters."), "environment": contract.StringSchema("Lowercase environment slug, max 12 characters."), "vector": map[string]any{"type": "boolean", "description": "Request pgvector for the new app database."}}
	op := func(name, description string, schema map[string]any, output map[string]any) contract.Operation {
		return contract.Operation{Name: name, Description: description, Effect: contract.EffectRead, Target: contract.TargetDescriptor{Kind: "postgres.cluster", From: []string{"cluster"}}, Preview: contract.PreviewNone, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: contract.LocalFSNone, InputSchema: schema, OutputSchema: output}
	}
	ops := []contract.Operation{
		op("inspect", "Read bounded cluster metadata only; never application rows or password hashes.", contract.ObjectSchema(common, "cluster"), contract.OutputSchemaOf[Inventory]()),
		op("plan_app", "Preview app isolation, role split, credential requirements and blockers. Does not provision or generate credentials.", contract.ObjectSchema(plan, "cluster", "app", "environment"), contract.OutputSchemaOf[Plan]()),
	}
	for _, name := range []string{"provision_app", "adopt_app", "rotate_login", "retire_login", "revoke_login", "retire_legacy"} {
		inputs := map[string]any{"cluster": common["cluster"], "app": plan["app"], "environment": plan["environment"], "expected_plan": contract.StringSchema("Lifecycle plan_digest from this operation's dry-run; required for execution.")}
		required := []string{"cluster", "app", "environment"}
		slots := []string(nil)
		description := ""
		switch name {
		case "provision_app":
			inputs["vector"] = plan["vector"]
			slots = []string{"migration_password", "runtime_password"}
			description = "Provision a new private app database and separate migration/runtime logins. Refuses adoption and existing PUBLIC CONNECT. Host persists credentials first; return references only."
		case "adopt_app":
			inputs["adoption"] = adoptionInputSchema()
			required = append(required, "adoption")
			slots = []string{"migration_password", "runtime_password"}
			description = "Adopt an existing dedicated database with an explicit table privilege mapping; preserve data and names. Refuse unsupported objects and stale plans. Host persists credentials before transactional ownership/grant changes and activation."
		case "retire_legacy":
			inputs["consumer"] = contract.StringSchema("Live replacement runtime application_name; lowercase slug, max 48 characters.")
			required = append(required, "consumer")
			description = "Revoke legacy CONNECT and grants only in the adopted database, then drain its sessions there after proving the new runtime consumer. Preserve the legacy role and access to other databases."
		case "rotate_login":
			slots = []string{"password"}
			description = "Add a new login generation with a host-persisted credential; retain existing generations until consumer cutover."
		case "retire_login":
			inputs["consumer"] = contract.StringSchema("Replacement connection application_name proving consumer cutover; lowercase slug, max 48 characters.")
			required = append(required, "consumer")
			description = "Disable and drain an old login only while a newer generation has a live named consumer connection. Preserves roles and data."
		case "revoke_login":
			description = "Emergency disable and drain a managed login without requiring a replacement. Preserves roles and data."
		}
		if name != "provision_app" && name != "adopt_app" && name != "retire_legacy" {
			inputs["kind"] = map[string]any{"type": "string", "enum": []string{"runtime", "migration"}}
			required = append(required, "kind")
		}
		if name == "retire_login" || name == "revoke_login" {
			inputs["generation"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 999999}
			required = append(required, "generation")
		}
		fs := contract.LocalFSNone
		if len(slots) > 0 {
			inputs["issuance_id"] = contract.StringSchema("Fresh 32-character lowercase hex ID; host durably refuses reuse.")
			required = append(required, "issuance_id")
			fs = contract.LocalFSWrites
		}
		ops = append(ops, contract.Operation{Name: name, Description: description, Effect: contract.EffectAdmin, Target: contract.TargetDescriptor{Kind: "postgres.cluster", From: []string{"cluster"}}, Preview: contract.PreviewPlugin, Output: contract.OutputStructured, Cost: contract.CostNone, LocalFS: fs, IssuedSecrets: slots, InputSchema: contract.ObjectSchema(inputs, required...), OutputSchema: map[string]any{"oneOf": []any{contract.OutputSchemaOf[LifecyclePlan](), contract.OutputSchemaOf[LifecycleResult]()}}})
	}
	return contract.Finalize(contract.Definition{ID: ConnectorID, Version: Version, ResourceTypes: []string{"server"}, Config: contract.ConfigSchema{Fields: fields, Secrets: []contract.SecretRequirement{{Name: "password", Description: "Declared PostgreSQL catalog-reader credential, supplied privately by the host.", Required: true}, {Name: "admin_password", Required: true, Description: "Dedicated provisioner credential. Bind only in connector-secrets.yaml write; never in read or common credentials."}}}, Operations: ops})
}
func Manifest() contract.Manifest { return contract.ManifestFromDefinition(Definition()) }
