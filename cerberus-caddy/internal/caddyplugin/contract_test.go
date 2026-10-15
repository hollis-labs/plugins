package caddyplugin

import (
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/connector/conformance"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"gopkg.in/yaml.v3"
)

func generatedSpec(t *testing.T) cerbplugin.PluginYAML {
	t.Helper()
	data, err := yaml.Marshal(PluginYAML())
	if err != nil {
		t.Fatal(err)
	}
	var spec cerbplugin.PluginYAML
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestEveryOperationDeclaresACompleteContract(t *testing.T) {
	for _, op := range Definition().Operations {
		if err := op.Validate(); err != nil {
			t.Error(err)
		}
	}
	if gaps := Manifest().ContractGaps(); len(gaps) != 0 {
		t.Fatalf("contract gaps: %v", gaps)
	}
}

func TestManifestConforms(t *testing.T) {
	if problems := conformance.Definition(Definition()); len(problems) > 0 {
		t.Errorf("Definition does not conform:\n  %s", conformance.Report(problems))
	}
	if problems := conformance.Manifest(Manifest()); len(problems) > 0 {
		t.Errorf("Manifest does not conform:\n  %s", conformance.Report(problems))
	}
	if problems := conformance.Manifest(generatedSpec(t).Cerberus.Connector); len(problems) > 0 {
		t.Errorf("generated plugin.yaml does not conform:\n  %s", conformance.Report(problems))
	}
}

func TestEveryOperationLabelsItsOutput(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.OutputSchema == nil {
			t.Errorf("operation %q has no output_schema", op.Name)
			continue
		}
		if _, err := contract.OutputLabelPointers(op.OutputSchema); err != nil {
			t.Errorf("operation %q: %v", op.Name, err)
		}
	}
}

// apply is the one operation that writes a file: it must be ack-gated and
// previewable, and render must stay a plain read.
func TestWriteOperationsAreAckGatedAndPreviewed(t *testing.T) {
	ops := map[string]contract.Operation{}
	for _, op := range Definition().Operations {
		ops[op.Name] = op
	}
	apply := ops["apply"]
	if !apply.RequiresAck || !apply.SupportsDry || apply.Preview != contract.PreviewPlugin || apply.LocalFS != contract.LocalFSWrites {
		t.Errorf("apply = ack %v dry %v preview %s localfs %s", apply.RequiresAck, apply.SupportsDry, apply.Preview, apply.LocalFS)
	}
	if render := ops["render"]; render.RequiresAck || render.SupportsDry || render.Effect != contract.EffectRead {
		t.Errorf("render must be a plain read, got %+v", render)
	}
	if reload := ops["reload"]; !reload.RequiresAck {
		t.Error("reload must stay ack-gated")
	}
}

// The token is a declared secret, delivered over Init, and never a plain
// config field or a call argument.
func TestTokenIsADeclaredCredentialSecret(t *testing.T) {
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != SecretCloudflareToken || !secrets[0].IsCredential() {
		t.Fatalf("secrets = %+v", secrets)
	}
	for _, f := range Definition().Config.Fields {
		if f.Name == SecretCloudflareToken {
			t.Error("the token is also declared as a plain config field")
		}
	}
	for _, op := range Definition().Operations {
		if _, ok := op.InputSchema["properties"].(map[string]any)[SecretCloudflareToken]; ok {
			t.Errorf("operation %q takes the token as an argument", op.Name)
		}
	}
}

func TestSurfacesDoNotExposeApplyByDefault(t *testing.T) {
	for _, name := range PluginYAML().Cerberus.Surfaces.MCP {
		if name == "apply" {
			t.Error("apply is suggested for MCP exposure; leave that to the operator")
		}
	}
}
