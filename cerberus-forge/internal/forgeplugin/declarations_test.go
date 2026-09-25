package forgeplugin

import (
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// The install review shows no gaps for this plugin. These are the review's
// gap rules (internal/pluginhost/review.go in cerberus), applied to the
// generated plugin.yaml: every operation declares an effect and an output
// kind, every non-read declares its telemetry, and the host range is set.
func TestInstallReviewShowsNoGaps(t *testing.T) {
	spec := PluginYAML()
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	block := spec.Cerberus
	if gaps := block.Connector.ContractGaps(); len(gaps) != 0 {
		t.Errorf("contract gaps: %v", gaps)
	}
	telemetry := map[string]bool{}
	for _, decl := range block.Telemetry {
		telemetry[decl.Operation] = true
	}
	for _, op := range block.Connector.Operations {
		if op.Output == "" {
			t.Errorf("%s declares no output kind", op.Name)
		}
		if !op.EffectiveEffect().ReadOnly() && !telemetry[op.Name] {
			t.Errorf("%s changes something and declares no telemetry", op.Name)
		}
	}
	if block.Host != (cerbplugin.HostRange{MinContract: 1, MaxContract: 1}) {
		t.Errorf("host range %+v", block.Host)
	}
	if err := block.Host.Check(cerbplugin.ContractVersion); err != nil {
		t.Errorf("the current host is outside the declared range: %v", err)
	}
	// MCP is suggested for plain reads only, never for a CLI-only operation.
	cli := map[string]bool{}
	for _, name := range block.Surfaces.CLIOnly {
		cli[name] = true
	}
	for _, name := range block.Surfaces.MCP {
		op, _ := cerbplugin.OperationFromToolName(ConnectorID, cerbplugin.ToolNameForOperation(ConnectorID, name), block.Connector)
		if op.EffectiveEffect() != contract.EffectRead || cli[name] {
			t.Errorf("%s is suggested for MCP", name)
		}
	}
}

// Every non-read operation reports an event of a kind it declares, and a read
// reports nothing.
func TestOperationsReportTheirDeclaredTelemetry(t *testing.T) {
	declared := map[string]map[string]bool{}
	for _, decl := range PluginYAML().Cerberus.Telemetry {
		declared[decl.Operation] = map[string]bool{}
		for _, e := range decl.Events {
			declared[decl.Operation][e] = true
		}
	}
	for _, op := range Manifest().Operations {
		for _, dry := range []bool{false, true} {
			req := subprocess.MCPCallRequest{ToolName: cerbplugin.ToolNameForOperation(ConnectorID, op.Name), Arguments: map[string]any{argDryRun: dry}}
			out := withOperationTelemetry(req, subprocess.MCPCallResult{Content: []byte(`{"ok":true}`)})
			_, events := cerbplugin.SplitTelemetry(out.Content)
			if op.EffectiveEffect().ReadOnly() {
				if len(events) != 0 {
					t.Errorf("read %s reported %v", op.Name, events)
				}
				continue
			}
			if len(events) == 0 {
				t.Errorf("%s reported nothing", op.Name)
			}
			for _, e := range events {
				if !declared[op.Name][e.Kind] {
					t.Errorf("%s reported %q, which it does not declare", op.Name, e.Kind)
				}
			}
		}
	}
}
