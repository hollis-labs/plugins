// Package catalog defines the portfolio plugin directory and distribution
// contract. Checksums detect changed bytes; they do not authenticate publishers.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const SchemaVersion = 2

type Document struct {
	SchemaVersion  int      `json:"schema_version"`
	CatalogVersion string   `json:"catalog_version"`
	GeneratedAt    string   `json:"generated_at"`
	Plugins        []Plugin `json:"plugins"`
}

// Plugin carries install metadata and the directory's view of a generated
// manifest. Its summary is a declaration, never an approval or permission grant.
type Plugin struct {
	ID             string                        `json:"id"`
	Name           string                        `json:"name"`
	Description    string                        `json:"description,omitempty"`
	Version        string                        `json:"version"`
	License        string                        `json:"license,omitempty"`
	Homepage       string                        `json:"homepage,omitempty"`
	Repository     string                        `json:"repository,omitempty"`
	Hosts          map[string]manifest.HostRange `json:"hosts"`
	Source         Source                        `json:"source"`
	Archives       []Archive                     `json:"archives"`
	ManifestURL    string                        `json:"manifest_url,omitempty"`
	ManifestSHA256 string                        `json:"manifest_sha256"`
	Directory      Directory                     `json:"directory"`
	Summary        Summary                       `json:"summary"`
}

type Source struct {
	Type string `json:"type"`
	Repo string `json:"repo"`
	Tag  string `json:"tag"`
}

type Archive struct {
	Platform string `json:"platform"`
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// Directory holds presentation metadata, not installation policy or tiers.
type Directory struct {
	Icon        string   `json:"icon,omitempty"`
	Screenshots []string `json:"screenshots,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	ShortDesc   string   `json:"short_desc,omitempty"`
	ReadmeURL   string   `json:"readme_url,omitempty"`
	Status      string   `json:"status"` // active, deprecated, experimental
}

type Summary struct {
	Capabilities []subprocess.CapabilityRequest `json:"capabilities"`
	Operations   []Operation                    `json:"operations"`
	Effects      []string                       `json:"effects"`
}

type Operation struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Effect      string `json:"effect"`
	Host        string `json:"host,omitempty"` // absent for shared tools
}

// FromManifest derives identity, compatibility and summaries from the exact
// generated manifest bytes. Presentation/source/archive information is supplied
// by release tooling, never guessed from an absent release or checksum.
func FromManifest(raw []byte, source Source, archives []Archive, manifestURL string, directory Directory) (Plugin, error) {
	m, err := manifest.Decode(strings.NewReader(string(raw)))
	if err != nil {
		return Plugin{}, err
	}
	digest := sha256.Sum256(raw)
	p := Plugin{ID: m.ID, Name: m.Name, Description: m.Description, Version: m.Version, License: m.License, Homepage: m.Homepage, Repository: m.Repository, Hosts: m.Hosts, Source: source, Archives: archives,
		ManifestURL: manifestURL, ManifestSHA256: hex.EncodeToString(digest[:]), Directory: directory,
		Summary: Summary{Capabilities: append([]subprocess.CapabilityRequest{}, m.Capabilities...), Operations: []Operation{}, Effects: []string{}},
	}
	for _, tool := range m.Tools {
		p.Summary.Operations = append(p.Summary.Operations, Operation{Name: tool.Name, Description: tool.Description, Effect: tool.Effect})
	}
	// Cerberus connector operations have their own richer host-owned contract.
	// Read only the directory fields and leave the remaining contract to that
	// host; do not infer a benign effect for an undeclared operation.
	if len(m.Cerberus) > 0 {
		var ext struct {
			Connector struct {
				Operations []struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Effect      string `json:"effect"`
				} `json:"operations"`
			} `json:"connector"`
		}
		if err := json.Unmarshal(m.Cerberus, &ext); err != nil {
			return Plugin{}, fmt.Errorf("catalog: cerberus summary: %w", err)
		}
		for _, op := range ext.Connector.Operations {
			if strings.TrimSpace(op.Name) == "" || strings.TrimSpace(op.Effect) == "" {
				return Plugin{}, fmt.Errorf("catalog: cerberus operation must declare name and effect")
			}
			p.Summary.Operations = append(p.Summary.Operations, Operation{Name: op.Name, Description: op.Description, Effect: op.Effect, Host: "cerberus"})
		}
	}
	for _, op := range p.Summary.Operations {
		if !slices.Contains(p.Summary.Effects, op.Effect) {
			p.Summary.Effects = append(p.Summary.Effects, op.Effect)
		}
	}
	slices.Sort(p.Summary.Effects)
	if err := p.Validate(); err != nil {
		return Plugin{}, err
	}
	return p, nil
}

func (d Document) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("catalog: schema_version must be %d", SchemaVersion)
	}
	if !manifest.ValidVersion(d.CatalogVersion) {
		return fmt.Errorf("catalog: catalog_version must be SemVer")
	}
	if _, err := time.Parse(time.RFC3339, d.GeneratedAt); err != nil {
		return fmt.Errorf("catalog: generated_at must be RFC3339")
	}
	if d.Plugins == nil {
		return fmt.Errorf("catalog: plugins must be an array")
	}
	seen := map[string]bool{}
	for _, p := range d.Plugins {
		if seen[p.ID] {
			return fmt.Errorf("catalog: duplicate plugin %q", p.ID)
		}
		seen[p.ID] = true
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p Plugin) Validate() error {
	fail := func(message string) error { return fmt.Errorf("catalog plugin %q: %s", p.ID, message) }
	if !manifest.ValidID(p.ID) || strings.TrimSpace(p.Name) == "" || !manifest.ValidVersion(p.Version) {
		return fail("invalid identity or version")
	}
	if len(p.Hosts) == 0 {
		return fail("hosts is required")
	}
	for host, r := range p.Hosts {
		if !manifest.ValidID(host) || (r.Min == "" && r.Max == "") || (r.Min != "" && !manifest.ValidVersion(r.Min)) || (r.Max != "" && !manifest.ValidVersion(r.Max)) {
			return fail("invalid host range")
		}
	}
	if p.Source.Type != "git" || !https(p.Source.Repo) || strings.TrimSpace(p.Source.Tag) == "" {
		return fail("source must name a git repository HTTPS URL and release tag")
	}
	if !digest(p.ManifestSHA256) || (p.ManifestURL != "" && !https(p.ManifestURL)) {
		return fail("manifest requires a SHA-256 digest and optional HTTPS URL")
	}
	if len(p.Archives) == 0 {
		return fail("archives is required")
	}
	seen := map[string]bool{}
	for _, a := range p.Archives {
		if !slices.Contains([]string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64", "windows-amd64", "windows-arm64"}, a.Platform) || seen[a.Platform] {
			return fail("invalid or duplicate archive platform")
		}
		seen[a.Platform] = true
		if !https(a.URL) || !digest(a.SHA256) || a.Size < 1 {
			return fail("archive requires HTTPS URL, SHA-256 and positive size")
		}
	}
	if !slices.Contains([]string{"active", "deprecated", "experimental"}, p.Directory.Status) {
		return fail("invalid directory status")
	}
	for _, u := range append([]string{p.Directory.Icon, p.Directory.ReadmeURL}, p.Directory.Screenshots...) {
		if u != "" && !https(u) {
			return fail("directory asset URLs must use HTTPS")
		}
	}
	return nil
}

func https(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// Decode refuses legacy fields, duplicate keys and unknown schema versions.
// A consumer must still verify archive bytes and its own host compatibility.
func Decode(raw []byte) (Document, error) {
	var d Document
	if err := manifest.DecodeExtension(raw, &d); err != nil {
		return Document{}, err
	}
	if err := d.Validate(); err != nil {
		return Document{}, err
	}
	return d, nil
}
