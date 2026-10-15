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
	{Operation: "stop", Require: cerbplugin.RequireApprovalForAgents, Reason: "takes a running droplet offline; a stopped droplet still bills"},
	{Operation: "create_firewall", Require: cerbplugin.RequireApprovalForAgents, Reason: "sets what the internet can reach; a rule admitting 0.0.0.0/0 or ::/0 breaks the tailnet-only posture"},
	{Operation: "update_firewall", Require: cerbplugin.RequireApprovalForAgents, Reason: "replaces firewall rules; a rule admitting 0.0.0.0/0 or ::/0 breaks the tailnet-only posture"},
	{Operation: "delete_firewall", Require: cerbplugin.RequireApproval, Reason: "removes the protection from every droplet it covers"},
	{Operation: "unassign_firewall_droplets", Require: cerbplugin.RequireApprovalForAgents, Reason: "removes a droplet's firewall protection"},
	{Operation: "assign_tag", Require: cerbplugin.RequireApprovalForAgents, Reason: "a tag can pull a droplet under, or out of, a tag-targeted firewall"},
	{Operation: "create_volume", Require: cerbplugin.RequireApprovalForAgents, Reason: "creates billable storage"},
	{Operation: "resize_volume", Require: cerbplugin.RequireApprovalForAgents, Reason: "raises a volume's cost and cannot be shrunk"},
	{Operation: "create_volume_snapshot", Require: cerbplugin.RequireApprovalForAgents, Reason: "creates billable storage"},
	{Operation: "create_droplet_snapshot", Require: cerbplugin.RequireApprovalForAgents, Reason: "creates billable storage"},
	{Operation: "detach_volume", Require: cerbplugin.RequireApprovalForAgents, Reason: "detaching a mounted volume can corrupt it"},
	{Operation: "delete_volume", Require: cerbplugin.RequireApproval, Reason: "destroys the data on the volume; it cannot be undone"},
	{Operation: "delete_snapshot", Require: cerbplugin.RequireApproval, Reason: "destroys a backup; it cannot be undone"},
	{Operation: "delete_ssh_key", Require: cerbplugin.RequireApproval, Reason: "removes an account SSH key"},
	{Operation: "assign_reserved_ip", Require: cerbplugin.RequireApprovalForAgents, Reason: "moves live traffic to another droplet"},
	{Operation: "unassign_reserved_ip", Require: cerbplugin.RequireApprovalForAgents, Reason: "stops traffic to the IP"},
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
