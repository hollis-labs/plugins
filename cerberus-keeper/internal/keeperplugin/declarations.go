package keeperplugin

import (
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// declare adds the install review declarations to the generated plugin.yaml.
// status is the only operation, and a plain offline read, so it is the MCP
// suggestion. The secret_backend claim (scheme keeper) joins this block once
// the host's plugin.yaml schema carries it; until then the host loads this
// plugin as an ordinary connector and routes no references to it.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: []string{"status"}}
	return spec
}
