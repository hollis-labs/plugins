package sysmonplugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/connector/conformance"
	"gopkg.in/yaml.v3"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Every sysmon operation only inspects the host.
func TestEveryOperationIsAReadWithAStructuredSchema(t *testing.T) {
	ops := Definition().Operations
	if len(ops) != 5 {
		t.Fatalf("expected cpu, memory, disk, host, host_report; got %d operations", len(ops))
	}
	for _, op := range ops {
		if err := op.Validate(); err != nil {
			t.Error(err)
		}
		if op.Effect != contract.EffectRead {
			t.Errorf("%s: effect %s, want read", op.Name, op.Effect)
		}
		if op.OutputSchema["type"] != "object" {
			t.Errorf("%s: output schema type %v, want object", op.Name, op.OutputSchema["type"])
		}
	}
}

func TestManifestConforms(t *testing.T) {
	if problems := conformance.Definition(Definition()); len(problems) > 0 {
		t.Errorf("Definition does not conform:\n  %s", conformance.Report(problems))
	}
	data, err := yaml.Marshal(PluginYAML())
	if err != nil {
		t.Fatal(err)
	}
	var spec cerbplugin.PluginYAML
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if problems := conformance.Manifest(spec.Cerberus.Connector); len(problems) > 0 {
		t.Errorf("generated plugin.yaml does not conform:\n  %s", conformance.Report(problems))
	}
}
