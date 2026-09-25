package k8splugin

import (
	"fmt"
	"os"
	"path/filepath"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

// BinaryName is the entrypoint the host launches.
const BinaryName = "cerberus-kubernetes-plugin"

// PluginYAML is generated from Definition() rather than hand-written, so the
// manifest the host installs cannot drift from the operations we actually
// serve.
func PluginYAML() cerbplugin.PluginYAML {
	return declare(cerbplugin.PluginYAMLFromManifest(Manifest(), cerbplugin.Entrypoint{
		Command: filepath.ToSlash(filepath.Join("bin", BinaryName)),
	}))
}

// WriteDist lays out an installable plugin directory: plugin.yaml beside a
// bin/ the build drops the entrypoint into.
func WriteDist(dir string) error {
	if dir == "" {
		return fmt.Errorf("dist directory is required")
	}
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		return fmt.Errorf("create dist directories: %w", err)
	}

	spec := PluginYAML()
	if err := spec.Validate(dir); err != nil {
		return fmt.Errorf("generated plugin.yaml is invalid: %w", err)
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal plugin.yaml: %w", err)
	}
	path := filepath.Join(dir, cerbplugin.PluginYAMLFilename)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write plugin.yaml: %w", err)
	}
	return nil
}
