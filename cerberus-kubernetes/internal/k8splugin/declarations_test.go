package k8splugin

import (
	"testing"

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

// A write's Change becomes one event per field, and one per warning; a dry
// run's are previews.
func TestChangeBecomesTelemetry(t *testing.T) {
	events := changeEvents(Change{Operation: "scale_workload", Target: "Deployment/prod/web", Applied: true,
		Changes: []FieldChange{{Field: "replicas", Before: "2", After: "3"}}, Warnings: []string{"no PDB"}})
	if len(events) != 2 || events[0].Kind != eventChange || events[0].Message != "replicas: 2 -> 3" || events[0].Target != "Deployment/prod/web" || events[1].Kind != eventWarning {
		t.Fatalf("events %+v", events)
	}
	dry := changeEvents(Change{Operation: "delete_pod", Target: "Pod/prod/web-1", DryRun: true})
	if len(dry) != 1 || dry[0].Kind != eventPreview {
		t.Fatalf("dry run events %+v", dry)
	}
	declared := map[string]bool{}
	for _, decl := range PluginYAML().Cerberus.Telemetry {
		for _, e := range decl.Events {
			declared[e] = true
		}
	}
	for _, e := range append(events, dry...) {
		if !declared[e.Kind] {
			t.Errorf("%q is reported but not declared", e.Kind)
		}
	}
}
