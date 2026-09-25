package digitaloceanplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The install review declarations (plugin.yaml's cerberus block). Each is
// this plugin's claim about itself: the host shows it and uses it only to
// narrow what the plugin can reach.

// suggestedPolicy is what this plugin suggests the operator require. The
// host never applies it on the plugin's word.
var suggestedPolicy = []cerbplugin.SuggestedRule{
	{Operation: "create_droplet", Require: cerbplugin.RequireApprovalForAgents, Reason: "creates billable infrastructure"},
	{Operation: "destroy", Require: cerbplugin.RequireApproval, Reason: "destroys a droplet and its disk; it cannot be undone"},
	{Operation: "stop", Require: cerbplugin.RequireApprovalForAgents, Reason: "takes a running droplet offline"},
}

// cliOnly operations never reach MCP, whatever connector-config.yaml says.
var cliOnly = []string{}

// declare adds the review declarations to the generated plugin.yaml.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.SuggestedPolicy = suggestedPolicy
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: suggestedMCP(spec.Cerberus.Connector), CLIOnly: cliOnly}
	spec.Cerberus.Telemetry = telemetryDeclarations(spec.Cerberus.Connector)
	return spec
}

// suggestedMCP is the exposure this plugin suggests: its plain reads. Reads
// that return free text, and everything that changes something, are left to
// the operator to expose deliberately.
func suggestedMCP(m contract.Manifest) []string {
	var out []string
	for _, op := range m.Operations {
		if op.EffectiveEffect() == contract.EffectRead {
			out = append(out, op.Name)
		}
	}
	return out
}

// telemetryDeclarations names the events every non-read operation reports
// (withOperationTelemetry): a change, and a preview for one with a dry run.
func telemetryDeclarations(m contract.Manifest) []cerbplugin.TelemetryDeclaration {
	var out []cerbplugin.TelemetryDeclaration
	for _, op := range m.Operations {
		if op.EffectiveEffect().ReadOnly() {
			continue
		}
		events := []string{eventChange}
		if op.EffectivePreview() != contract.PreviewNone {
			events = append(events, eventPreview)
		}
		out = append(out, cerbplugin.TelemetryDeclaration{Operation: op.Name, Events: events})
	}
	return out
}
