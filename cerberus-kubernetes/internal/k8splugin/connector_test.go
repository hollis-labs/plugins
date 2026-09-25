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

// The host's --ack gate applies to an operation only if the manifest marks it
// Destructive, so a write that forgot the flag would run unacknowledged. This
// holds the manifest and writeOperations in step in both directions: every
// write is Destructive and SupportsDry, and nothing else is either — marking a
// read destructive would make it demand --ack and empty that gate of meaning.
//
// Adding a write means adding it to writeOperations; this test is what makes
// that a deliberate act rather than a flag someone may or may not remember.
func TestWriteOperationsAreExactlyTheDestructiveOnes(t *testing.T) {
	declared := map[string]bool{}
	for _, op := range Definition().Operations {
		declared[op.Name] = true
		write := writeOperations[op.Name]
		if write && !op.Destructive {
			t.Errorf("write operation %q is not marked Destructive; the host would run it without --ack", op.Name)
		}
		if write && !op.SupportsDry {
			t.Errorf("write operation %q does not declare SupportsDry; every write here previews with dryRun=All", op.Name)
		}
		if !write && op.Destructive {
			t.Errorf("operation %q is marked Destructive but is not in writeOperations", op.Name)
		}
		if !write && op.SupportsDry {
			t.Errorf("operation %q declares dry-run support, which only makes sense for a write", op.Name)
		}
	}
	for name := range writeOperations {
		if !declared[name] {
			t.Errorf("writeOperations names %q, which the manifest does not declare", name)
		}
	}
}

// The manifest the host installs must carry RequiresAck for every write, since
// that — not Destructive alone — is the field the host's policy checks.
func TestManifestRequiresAcknowledgmentForEveryWrite(t *testing.T) {
	for _, op := range Manifest().Operations {
		if writeOperations[op.Name] != op.RequiresAck {
			t.Errorf("operation %q: RequiresAck = %v, want %v", op.Name, op.RequiresAck, writeOperations[op.Name])
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
