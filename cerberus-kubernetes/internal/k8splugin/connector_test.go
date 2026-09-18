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

// "Work infrastructure is read-only" is a scope decision in AGENTS.md. Every
// operation here is a read, so none may be marked destructive — and marking a
// read destructive would make it demand --ack, emptying that gate of meaning
// for the operations that will one day genuinely need it.
//
// When WP-K6 unlocks writes, this test is the thing that should fail first and
// force a deliberate change, rather than a write slipping in unannounced.
func TestEveryOperationIsReadOnly(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.Destructive {
			t.Errorf("operation %q is marked destructive; this connector is read-only (see WP-K6)", op.Name)
		}
		if op.SupportsDry {
			t.Errorf("operation %q declares dry-run support, which only makes sense for a write", op.Name)
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
