package main

import (
	"os"
	"path/filepath"
	"testing"

	catalog "github.com/hollis-labs/nanite-plugins-catalog"
)

func TestBuilderValidatesBeforeReplacingOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Mkdir("dist", 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("dist", "catalog.yaml")
	if err := os.WriteFile(target, []byte("preserve previous output"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("plugins.json", []byte(`{"catalog_version":"2.0.0","plugins":[{"manifest":"missing.json"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("missing manifest accepted")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "preserve previous output" {
		t.Fatal("failed build destroyed previous output")
	}
	if err := os.WriteFile("plugins.json", []byte(`{"catalog_version":"2.0.0","plugins":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	d, err := catalog.Decode(raw)
	if err != nil || d.SchemaVersion != 2 || len(d.Plugins) != 0 {
		t.Fatalf("got %+v: %v", d, err)
	}
}

func TestBuilderRefusesLegacySeeds(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("plugins.json", []byte(`{"catalog_version":"2.0.0","plugins":[],"tier":"core"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("legacy seeds accepted")
	}
}
