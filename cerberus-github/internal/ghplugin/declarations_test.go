package ghplugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The install review shows no gaps for this plugin. These are the review's
// gap rules (internal/pluginhost/review.go in cerberus), applied to the
// generated plugin.yaml: every operation declares an effect and an output
// kind, and the host range is set. Every operation is a read, so none needs
// telemetry.
func TestInstallReviewShowsNoGaps(t *testing.T) {
	spec := PluginYAML()
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	block := spec.Cerberus
	if gaps := block.Connector.ContractGaps(); len(gaps) != 0 {
		t.Errorf("contract gaps: %v", gaps)
	}
	for _, op := range block.Connector.Operations {
		if op.Output == "" {
			t.Errorf("%s declares no output kind", op.Name)
		}
		if op.EffectiveEffect() != contract.EffectRead {
			t.Errorf("%s is not a plain read; it needs telemetry, policy and a surfaces review", op.Name)
		}
	}
	if block.Host != (cerbplugin.HostRange{MinContract: 1, MaxContract: 1}) {
		t.Errorf("host range %+v", block.Host)
	}
	if err := block.Host.Check(cerbplugin.ContractVersion); err != nil {
		t.Errorf("the current host is outside the declared range: %v", err)
	}
	if len(block.Surfaces.MCP) != len(block.Connector.Operations) {
		t.Errorf("suggested MCP %v, want every read", block.Surfaces.MCP)
	}
}

// The declared secret keeps the built-in's name and variable, so existing
// credential references resolve with no migration.
func TestSecretKeepsTheBuiltInsKey(t *testing.T) {
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != "token" || secrets[0].Env != "CERBERUS_GITHUB_TOKEN" || !secrets[0].Required {
		t.Fatalf("secrets = %+v", secrets)
	}
}
