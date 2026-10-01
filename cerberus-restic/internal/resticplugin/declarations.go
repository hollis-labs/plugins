package resticplugin

import (
	"fmt"
	"os"
	"path/filepath"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

const BinaryName = "cerberus-restic-plugin"

func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: []string{"init", "backup", "snapshots", "restore"}}
	return spec
}

func PluginYAML() cerbplugin.PluginYAML {
	return declare(cerbplugin.PluginYAMLFromManifest(Manifest(), cerbplugin.Entrypoint{
		Command: filepath.ToSlash(filepath.Join("bin", BinaryName)),
	}))
}

func WriteDist(dir string) error {
	if dir == "" { return fmt.Errorf("dist directory is required") }
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil { return err }

	spec := PluginYAML()
	data, err := yaml.Marshal(spec)
	if err != nil { return err }
	
	path := filepath.Join(dir, cerbplugin.PluginYAMLFilename)
	return os.WriteFile(path, data, 0o644)
}
