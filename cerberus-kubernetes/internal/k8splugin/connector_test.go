package k8splugin

import (
	"os"
	"path/filepath"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

func TestManifestIsValid(t *testing.T) {
	if err := Manifest().Validate(); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
}

// A plugin claiming a built-in connector id is refused at install: it would
// shadow the connector Cerberus serves itself and, because the secret channel
// namespaces by connector id, would be handed that connector's credentials.
func TestConnectorIDIsNotReserved(t *testing.T) {
	for _, reserved := range []string{"ssh", "docker", "local", "github"} {
		if ConnectorID == reserved {
			t.Fatalf("connector id %q is reserved for a built-in", ConnectorID)
		}
	}
}

// Every write is acknowledgment-gated and previews with dryRun=All, and no
// read is either: the contract derives both from effect and preview, so this
// holds the declared effects and writeOperations in step in both directions.
// Marking a read as a write would make it demand --ack and empty that gate of
// meaning; a write declared as a read would run unacknowledged.
//
// Adding a write means adding it to writeOperations; this test is what makes
// that a deliberate act rather than a field someone may or may not remember.
func TestWriteOperationsAreExactlyTheAckGatedOnes(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range Definition().Operations {
		declared[op.Name] = true
		write := writeOperations[op.Name]
		if write != !op.Effect.ReadOnly() {
			t.Errorf("operation %q: effect %q, but writeOperations says write=%v", op.Name, op.Effect, write)
		}
		if write != op.RequiresAck {
			t.Errorf("operation %q: RequiresAck = %v, want %v", op.Name, op.RequiresAck, write)
		}
		if write != op.SupportsDry {
			t.Errorf("operation %q: SupportsDry = %v, want %v; every write here previews with dryRun=All and no read does", op.Name, op.SupportsDry, write)
		}
	}
	for name := range writeOperations {
		if !declared[name] {
			t.Errorf("writeOperations names %q, which the manifest does not declare", name)
		}
	}
}

// The installed manifest carries the pre-contract flags too, derived from the
// contract, so a host that predates effect still gates every write: it reads
// destructive, which ManifestFromDefinition sets for every ack-gated op.
func TestManifestKeepsLegacyGateFlagsForOlderHosts(t *testing.T) {
	for _, op := range Manifest().Operations {
		write := writeOperations[op.Name]
		if op.RequiresAck != write || op.Destructive != write || op.SupportsDry != write {
			t.Errorf("operation %q: destructive=%v requires_ack=%v supports_dry=%v, want all %v",
				op.Name, op.Destructive, op.RequiresAck, op.SupportsDry, write)
		}
	}
}

func TestEveryOperationIsDocumentedAndSchemad(t *testing.T) {
	ops := Definition().Operations
	if len(ops) == 0 {
		t.Fatal("no operations declared")
	}
	for _, op := range ops {
		if op.Description == "" {
			t.Errorf("operation %q has no description; it becomes an MCP tool description an agent reads", op.Name)
		}
		if len(op.Examples) == 0 {
			t.Errorf("operation %q has no example", op.Name)
		}
		if op.InputSchema == nil {
			t.Errorf("operation %q has no input schema", op.Name)
		}
	}
}

func TestWriteDistGeneratesAnInstallableDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDist(dir); err != nil {
		t.Fatalf("WriteDist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, cerbplugin.PluginYAMLFilename)); err != nil {
		t.Fatalf("plugin.yaml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin")); err != nil {
		t.Fatalf("bin/ not created: %v", err)
	}
	if err := PluginYAML().Validate(dir); err != nil {
		t.Fatalf("generated plugin.yaml invalid: %v", err)
	}
}
