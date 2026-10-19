// Command build-catalog derives the portfolio catalog from generated manifests
// and explicit release metadata. It performs no network calls or signing.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	catalog "github.com/hollis-labs/plugins-catalog"
	"github.com/hollis-labs/plugin-sdk/manifest"
)

type seedFile struct {
	CatalogVersion string       `json:"catalog_version"`
	Plugins        []seedPlugin `json:"plugins"`
}

type seedPlugin struct {
	Manifest    string            `json:"manifest"`
	ManifestURL string            `json:"manifest_url,omitempty"`
	Source      catalog.Source    `json:"source"`
	Archives    []catalog.Archive `json:"archives"`
	Directory   catalog.Directory `json:"directory"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "build-catalog: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	raw, err := os.ReadFile("plugins.json")
	if err != nil {
		return err
	}
	var seed seedFile
	if err := manifest.DecodeExtension(raw, &seed); err != nil {
		return fmt.Errorf("plugins.json: %w", err)
	}
	if seed.Plugins == nil {
		return fmt.Errorf("plugins.json: plugins must be an array")
	}
	doc := catalog.Document{SchemaVersion: catalog.SchemaVersion, CatalogVersion: seed.CatalogVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Plugins: []catalog.Plugin{}}
	for _, s := range seed.Plugins {
		if s.Manifest == "" || !filepath.IsLocal(s.Manifest) {
			return fmt.Errorf("manifest must name a local generated file")
		}
		raw, err := os.ReadFile(s.Manifest)
		if err != nil {
			return err
		}
		p, err := catalog.FromManifest(raw, s.Source, s.Archives, s.ManifestURL, s.Directory)
		if err != nil {
			return err
		}
		doc.Plugins = append(doc.Plugins, p)
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		return err
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	// JSON is valid YAML. Validate the entire output against the JSON Schema in
	// tests/CI; build output remains a single schema-v2 document.
	tmp, err := os.CreateTemp("dist", ".catalog-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), "dist/catalog.yaml"); err != nil {
		return err
	}
	fmt.Printf("wrote dist/catalog.yaml (%d plugins)\n", len(doc.Plugins))
	return nil
}
