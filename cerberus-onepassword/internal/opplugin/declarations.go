package opplugin

import (
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// declare adds the install review declarations to the generated plugin.yaml.
// status is the only operation, and a plain offline read, so it is the MCP
// suggestion. secret_backend claims the op:// scheme: the host routes
// those references here, and the install review says this plugin will see
// every secret resolved through it.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: []string{"status"}}
	spec.Cerberus.SecretBackend = &cerbplugin.SecretBackend{Scheme: Scheme, Reference: "op://<vault>/<item>/<field>"}
	return spec
}
