package k8splugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points KUBECONFIG at an empty file for the whole package. Without
// it, any test that leaves the kubeconfig unset reads the developer's own
// ~/.kube/config — namespace resolution consults the current context — and a
// result would depend on which cluster the person running the tests last used.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "k8splugin-test")
	if err != nil {
		panic(err)
	}
	empty := filepath.Join(dir, "config")
	if err := os.WriteFile(empty, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		panic(err)
	}
	_ = os.Setenv("KUBECONFIG", empty)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
