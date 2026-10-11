package tailscaleplugin

import (
	"fmt"
	"os"
	"path/filepath"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

const BinaryName = "cerberus-tailscale-plugin"

// suggestedPolicy is what this plugin suggests the operator require. The host
// never applies it on the plugin's word.
var suggestedPolicy = []cerbplugin.SuggestedRule{
	{Operation: "mint_auth_key", Require: cerbplugin.RequireApproval, Reason: "mints a credential that joins a machine to the tailnet, and hands its value to the caller"},
	{Operation: "revoke_key", Require: cerbplugin.RequireApprovalForAgents, Reason: "disables a key that machines may be using to join"},
	{Operation: "delete_device", Require: cerbplugin.RequireApproval, Reason: "removes a device from the tailnet; it may not be able to rejoin without a new key"},
	{Operation: "set_tags", Require: cerbplugin.RequireApprovalForAgents, Reason: "tags decide which ACL rules apply to a device, so this can widen its access"},
	{Operation: "authorize_device", Require: cerbplugin.RequireApprovalForAgents, Reason: "admits a device to the tailnet"},
	{Operation: "up", Require: cerbplugin.RequireApprovalForAgents, Reason: "changes this host's tailnet connection"},
	{Operation: "down", Require: cerbplugin.RequireApprovalForAgents, Reason: "drops this host's tailnet connection, which may be the one carrying the session"},
	{Operation: "serve", Require: cerbplugin.RequireApprovalForAgents, Reason: "exposes a local port to the tailnet or, with funnel, the internet"},
}

// cliOnly operations never reach MCP, whatever connector-config.yaml says.
var cliOnly = []string{}

// declare adds the install review declarations to the generated plugin.yaml.
func declare(spec cerbplugin.PluginYAML) cerbplugin.PluginYAML {
	spec.Cerberus.Host = cerbplugin.HostRange{MinContract: 1, MaxContract: 1}
	spec.Cerberus.SuggestedPolicy = suggestedPolicy
	spec.Cerberus.Surfaces = cerbplugin.Surfaces{MCP: suggestedMCP(spec.Cerberus.Connector), CLIOnly: cliOnly}
	spec.Cerberus.Telemetry = telemetryDeclarations(spec.Cerberus.Connector)
	return spec
}

// suggestedMCP is the exposure this plugin suggests: its plain reads, plus the
// local-CLI operations it has always listed. Minting, revoking and deleting are
// left to the operator to expose deliberately.
func suggestedMCP(m contract.Manifest) []string {
	var out []string
	for _, op := range m.Operations {
		switch op.Name {
		case "up", "down", "serve":
			out = append(out, op.Name)
		default:
			if op.EffectiveEffect() == contract.EffectRead {
				out = append(out, op.Name)
			}
		}
	}
	return out
}

// telemetryDeclarations names the events every non-read operation reports
// (withOperationTelemetry): a change, and a preview for one with a dry run.
func telemetryDeclarations(m contract.Manifest) []cerbplugin.TelemetryDeclaration {
	var out []cerbplugin.TelemetryDeclaration
	for _, op := range m.Operations {
		// The local-CLI operations answer with plain text, which has nowhere
		// to carry telemetry, so they declare none: their audit record is the
		// host's view only.
		if op.EffectiveEffect().ReadOnly() || localOps[op.Name] {
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
	if err := spec.Validate(dir); err != nil {
		return fmt.Errorf("generated plugin.yaml is invalid: %w", err)
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, cerbplugin.PluginYAMLFilename)
	return os.WriteFile(path, data, 0o644)
}
