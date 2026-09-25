package cloudflareplugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

// Every operation declares a complete contract: an effect, a preview, an
// output, a cost, local filesystem access and a target whose fields are real
// inputs. Without an effect the host treats an operation as exec and asks for
// --ack on every call, reads included, so a missing one is a regression.
func TestEveryOperationDeclaresACompleteContract(t *testing.T) {
	for _, op := range Definition().Operations {
		if err := op.Validate(); err != nil {
			t.Error(err)
		}
	}
	if gaps := Manifest().ContractGaps(); len(gaps) != 0 {
		t.Fatalf("manifest reports contract gaps: %v", gaps)
	}
}

// The plugin.yaml write-dist generates is what the host reads, so the
// contract has to survive into it, not only into Definition().
func TestGeneratedPluginYAMLCarriesTheContract(t *testing.T) {
	data, err := yaml.Marshal(PluginYAML())
	if err != nil {
		t.Fatalf("marshal plugin.yaml: %v", err)
	}
	var spec cerbplugin.PluginYAML
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse plugin.yaml: %v", err)
	}
	want := map[string]contract.Operation{}
	for _, op := range Definition().Operations {
		want[op.Name] = op
	}
	got := spec.Cerberus.Connector.Operations
	if len(got) != len(want) {
		t.Fatalf("plugin.yaml has %d operations, Definition has %d", len(got), len(want))
	}
	for _, op := range got {
		def, ok := want[op.Name]
		if !ok {
			t.Errorf("plugin.yaml operation %q is not in Definition", op.Name)
			continue
		}
		if op.Effect == "" || op.Effect != def.Effect || op.Preview != def.Preview || op.Output != def.Output ||
			op.Cost != def.Cost || op.LocalFS != def.LocalFS || op.Target.Kind != def.Target.Kind {
			t.Errorf("plugin.yaml %q contract = %+v, want the Definition's (effect %s, preview %s, output %s, target %s)",
				op.Name, op, def.Effect, def.Preview, def.Output, def.Target.Kind)
		}
	}
	if gaps := spec.Cerberus.Connector.ContractGaps(); len(gaps) != 0 {
		t.Fatalf("plugin.yaml reports contract gaps: %v", gaps)
	}
}
