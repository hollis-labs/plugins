package caddyplugin

import (
	"fmt"
	"os"
	"path/filepath"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

const BinaryName = "cerberus-caddy-plugin"

func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.SuggestedPolicy = []cerbplugin.SuggestedRule{
		{Operation: "apply", Require: cerbplugin.RequireApprovalForAgents, Reason: "replaces the Caddyfile and reloads the proxy in front of every routed service"},
	}
	// Reads and the existing reload stay exposed; apply is left for the
	// operator to expose deliberately.
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: []string{"reload", "validate", "render"}}
	return spec
}

func PluginYAML() cerbplugin.PluginYAML {
	return declare(cerbplugin.PluginYAMLFromManifest(Manifest(), cerbplugin.Entrypoint{
		Command: filepath.ToSlash(filepath.Join("bin", BinaryName)),
	}))
}

func WriteDist(dir string) error {
	if dir == "" {
		return fmt.Errorf("dist directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return err
	}

	spec := PluginYAML()
	data, err := yaml.Marshal(spec)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, cerbplugin.PluginYAMLFilename)
	return os.WriteFile(path, data, 0o644)
}
