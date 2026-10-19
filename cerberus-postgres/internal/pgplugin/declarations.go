package pgplugin

import (
	"fmt"
	"os"
	"path/filepath"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

const BinaryName = "cerberus-postgres-plugin"

func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: []string{"inspect", "plan_app", "provision_app", "rotate_login", "retire_login", "revoke_login", "adopt_app", "retire_legacy"}}
	spec.Capabilities = []subprocess.CapabilityRequest{{Name: cerbplugin.CredentialIssuanceCapability}}
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
