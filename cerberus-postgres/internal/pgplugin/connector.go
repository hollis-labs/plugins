package pgplugin

import contract "github.com/hollis-labs/cerberus/pkg/connector"

const ConnectorID = "postgres"
const Version = "0.1.0"

func Definition() contract.Definition {
	fields := []contract.ConfigField{}
	for _, f := range []struct{ name, description string }{
		{"cluster", "Operator-defined cluster identity; calls must match it."},
		{"host", "PostgreSQL DNS hostname (TLS verify-full), or absolute Unix socket directory."},
		{"port", "PostgreSQL port, 1..65535."},
		{"user", "Catalog reader username. No password or DSN in this field."},
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
	return contract.Finalize(contract.Definition{ID: ConnectorID, Version: Version, ResourceTypes: []string{"server"}, Config: contract.ConfigSchema{Fields: fields, Secrets: []contract.SecretRequirement{{Name: "password", Description: "Declared PostgreSQL catalog-reader credential, supplied privately by the host.", Required: true}}}, Operations: []contract.Operation{
		op("inspect", "Read bounded cluster metadata only; never application rows or password hashes.", contract.ObjectSchema(common, "cluster"), contract.OutputSchemaOf[Inventory]()),
		op("plan_app", "Preview app isolation, role split, credential requirements and blockers. Does not provision or generate credentials.", contract.ObjectSchema(plan, "cluster", "app", "environment"), contract.OutputSchemaOf[Plan]()),
	}})
}
func Manifest() contract.Manifest { return contract.ManifestFromDefinition(Definition()) }
