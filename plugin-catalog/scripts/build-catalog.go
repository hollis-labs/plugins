// Command build-catalog reads plugins.yaml and produces dist/catalog.yaml
// conforming to catalog.schema.v1.json.
//
// Per Track F.3 §builder logic:
//
//  1. Read plugins.yaml (seed list + tiers).
//  2. For each plugin with source.type=git, fetch its latest release tag.
//  3. Download/compute per-platform archive SHA-256 and (optional) sig URLs.
//  4. Assemble the catalog document.
//  5. Write dist/catalog.yaml. Signing is a separate step (scripts/sign-catalog.sh)
//     so the signing key never has to be handled by this builder.
//
// For the Phase 2 bootstrap the seed list is empty, so steps 2-3 are no-ops.
// They are wired up here as TODO stubs — flipping on GH release discovery
// is a single edit away once the first plugin (giphy, Track E) ships.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type seedFile struct {
	CatalogVersion string       `yaml:"catalog_version"`
	Plugins        []seedPlugin `yaml:"plugins"`
}

type seedPlugin struct {
	ID           string       `yaml:"id"`
	Name         string       `yaml:"name"`
	Description  string       `yaml:"description,omitempty"`
	Tier         string       `yaml:"tier"`
	Author       string       `yaml:"author,omitempty"`
	License      string       `yaml:"license,omitempty"`
	Homepage     string       `yaml:"homepage,omitempty"`
	NaniteCompat string       `yaml:"nanite_compat,omitempty"`
	Source       seedSource   `yaml:"source"`
	Archives     []seedArchive `yaml:"archives,omitempty"`
	// Optional override: pin a specific version instead of fetching latest tag.
	PinVersion string `yaml:"pin_version,omitempty"`
}

type seedSource struct {
	Type string `yaml:"type"`
	Repo string `yaml:"repo,omitempty"`
	Tag  string `yaml:"tag,omitempty"`
}

type seedArchive struct {
	Platform string `yaml:"platform"`
	URL      string `yaml:"url"`
	SHA256   string `yaml:"sha256"`
	SigURL   string `yaml:"sig_url,omitempty"`
	Size     int64  `yaml:"size,omitempty"`
}

type catalogDoc struct {
	SchemaVersion  int             `yaml:"schema_version"`
	CatalogVersion string          `yaml:"catalog_version"`
	GeneratedAt    string          `yaml:"generated_at"`
	Plugins        []catalogPlugin `yaml:"plugins"`
}

type catalogPlugin struct {
	ID           string           `yaml:"id"`
	Name         string           `yaml:"name"`
	Description  string           `yaml:"description,omitempty"`
	Version      string           `yaml:"version"`
	Tier         string           `yaml:"tier"`
	Author       string           `yaml:"author,omitempty"`
	License      string           `yaml:"license,omitempty"`
	Homepage     string           `yaml:"homepage,omitempty"`
	Source       seedSource       `yaml:"source"`
	NaniteCompat string           `yaml:"nanite_compat,omitempty"`
	Archives     []seedArchive    `yaml:"archives,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "build-catalog: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	rawSeed, err := os.ReadFile("plugins.yaml")
	if err != nil {
		return fmt.Errorf("read plugins.yaml: %w", err)
	}
	var seed seedFile
	if err := yaml.Unmarshal(rawSeed, &seed); err != nil {
		return fmt.Errorf("parse plugins.yaml: %w", err)
	}
	if seed.CatalogVersion == "" {
		return fmt.Errorf("plugins.yaml missing catalog_version")
	}

	doc := catalogDoc{
		SchemaVersion:  1,
		CatalogVersion: seed.CatalogVersion,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
		Plugins:        make([]catalogPlugin, 0, len(seed.Plugins)),
	}

	for _, p := range seed.Plugins {
		version := p.PinVersion
		if p.Source.Type == "git" && version == "" {
			// TODO(track-F.3 follow-up): fetch latest release tag via
			// `gh release view --repo <repo> --json tagName -q .tagName`.
			// For now require pin_version until the first plugin ships.
			return fmt.Errorf("plugin %q: source.type=git but no pin_version set and release-tag discovery is not implemented yet", p.ID)
		}
		if p.Source.Type == "builtin" && version == "" {
			version = "0.0.0"
		}

		archives := p.Archives
		if p.Source.Type == "git" && len(archives) == 0 {
			// TODO(track-F.3 follow-up): download each release asset, compute
			// sha256, pull the .sig sibling, and emit archive entries. Stubbed
			// to keep the builder deterministic for the bootstrap catalog.
			return fmt.Errorf("plugin %q: git source without archive list; archive auto-discovery not implemented yet", p.ID)
		}

		src := p.Source
		if src.Type == "git" && src.Tag == "" {
			src.Tag = "v" + version
		}

		doc.Plugins = append(doc.Plugins, catalogPlugin{
			ID:           p.ID,
			Name:         p.Name,
			Description:  p.Description,
			Version:      version,
			Tier:         p.Tier,
			Author:       p.Author,
			License:      p.License,
			Homepage:     p.Homepage,
			Source:       src,
			NaniteCompat: p.NaniteCompat,
			Archives:     archives,
		})
	}

	if err := os.MkdirAll("dist", 0o755); err != nil {
		return fmt.Errorf("mkdir dist: %w", err)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	// Prepend a human-readable header — consumers parse YAML so comments are safe.
	header := []byte("# Generated by scripts/build-catalog.go — do not edit by hand.\n" +
		"# Source: github.com/hollis-labs/nanite-plugins-catalog\n" +
		"# Schema: https://plugins.nanite.hollislabs.dev/catalog.schema.v1.json\n")
	body := append(header, out...)

	target := filepath.Join("dist", "catalog.yaml")
	if err := os.WriteFile(target, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	fmt.Printf("wrote %s (%d plugins, catalog_version=%s)\n",
		target, len(doc.Plugins), doc.CatalogVersion)
	return nil
}
