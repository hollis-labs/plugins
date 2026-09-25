package cfplugin

import (
	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The install review declarations (plugin.yaml's cerberus block). Each is
// this plugin's claim about itself: the host shows it and uses it only to
// narrow what the plugin can reach. Every operation here reads, so there is
// no telemetry to declare and nothing to keep off MCP.

// suggestedPolicy is what this plugin suggests the operator require. The
// host never applies it on the plugin's word.
var suggestedPolicy = []cerbplugin.SuggestedRule{
	{Operation: "list_tools", Require: cerbplugin.RequireApprovalForAgents, Reason: "returns tool descriptions written by the upstream servers: free text of unknown origin"},
}

// declare adds the review declarations to the generated plugin.yaml.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.SuggestedPolicy = suggestedPolicy
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: suggestedMCP(spec.Cerberus.Connector)}
	return spec
}

// suggestedMCP is the exposure this plugin suggests: its plain reads. Reads
// that return free text are left to the operator to expose deliberately.
func suggestedMCP(m contract.Manifest) []string {
	var out []string
	for _, op := range m.Operations {
		if op.EffectiveEffect() == contract.EffectRead {
			out = append(out, op.Name)
		}
	}
	return out
}
