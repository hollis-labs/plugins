package k8splugin

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
	{Operation: "cordon_node", Require: cerbplugin.RequireApprovalForAgents, Reason: "stops new pods being scheduled on the node"},
	{Operation: "delete_pod", Require: cerbplugin.RequireApproval, Reason: "a pod no controller owns does not come back"},
	{Operation: "get_logs", Require: cerbplugin.RequireApprovalForAgents, Reason: "container logs are free text and may carry secrets or personal data"},
	{Operation: "list_events", Require: cerbplugin.RequireApprovalForAgents, Reason: "event messages are free text written by cluster components"},
	{Operation: "restart_workload", Require: cerbplugin.RequireApprovalForAgents, Reason: "rolls every pod of the workload"},
	{Operation: "scale_workload", Require: cerbplugin.RequireApprovalForAgents, Reason: "changes the workload's capacity; scaling to zero takes it down"},
	{Operation: "uncordon_node", Require: cerbplugin.RequireApprovalForAgents, Reason: "returns the node to scheduling"},
}

// declare adds the review declarations to the generated plugin.yaml.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.SuggestedPolicy = suggestedPolicy
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: suggestedMCP(spec.Cerberus.Connector)}
	spec.Cerberus.Telemetry = telemetryDeclarations(spec.Cerberus.Connector)
	return spec
}

// suggestedMCP is the exposure this plugin suggests: its plain reads. Reads
// that return free text, and every write, are left to the operator to expose
// deliberately.
func suggestedMCP(m contract.Manifest) []string {
	var out []string
	for _, op := range m.Operations {
		if op.EffectiveEffect() == contract.EffectRead {
			out = append(out, op.Name)
		}
	}
	return out
}
