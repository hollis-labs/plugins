package catalog_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	catalog "github.com/hollis-labs/nanite-plugins-catalog"
	"github.com/hollis-labs/plugin-sdk/manifest"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func manifestBytes(t *testing.T) []byte {
	t.Helper()
	m := manifest.Manifest{SchemaVersion: manifest.SchemaVersion, ID: "hollis.bookmarks", Name: "Bookmarks", Version: "1.2.3", Protocol: subprocess.ProtocolVersion, Runtime: manifest.Runtime,
		Entrypoint: manifest.Entrypoint{Command: "bin/bookmarks"}, Hosts: map[string]manifest.HostRange{"nanite": {Min: "0.1.0"}, "cerberus": {Min: "1.0.0"}},
		Capabilities: []subprocess.CapabilityRequest{{Name: "host.query", Optional: true}},
		Tools:        []manifest.Tool{{Name: "bookmarks_list", Description: "List bookmarks", InputSchema: json.RawMessage(`{"type":"object"}`), Effect: "read"}},
		Cerberus:     json.RawMessage(`{"connector":{"operations":[{"name":"delete","description":"Delete bookmark","effect":"destructive"}]}}`),
	}
	var out bytes.Buffer
	if err := manifest.Encode(&out, m); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func plugin(t *testing.T) catalog.Plugin {
	t.Helper()
	p, err := catalog.FromManifest(manifestBytes(t), catalog.Source{Type: "git", Repo: "https://github.com/hollis-labs/nanite-plugins", Tag: "bookmarks/v1.2.3"},
		[]catalog.Archive{{Platform: "linux-amd64", URL: "https://github.com/hollis-labs/nanite-plugins/releases/download/bookmarks/v1.2.3/bookmarks-linux-amd64.tar.gz", SHA256: strings.Repeat("a", 64), Size: 12345}},
		"https://github.com/hollis-labs/nanite-plugins/releases/download/bookmarks/v1.2.3/plugin.yaml",
		catalog.Directory{Status: "active", ShortDesc: "Save links", Tags: []string{"bookmarks"}, Icon: "https://hollislabs.com/bookmarks.svg", Screenshots: []string{"https://hollislabs.com/bookmarks.png"}, ReadmeURL: "https://github.com/hollis-labs/nanite-plugins/tree/main/bookmarks"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func document(t *testing.T) catalog.Document {
	return catalog.Document{SchemaVersion: catalog.SchemaVersion, CatalogVersion: "2.0.0", GeneratedAt: "2026-10-01T00:00:00Z", Plugins: []catalog.Plugin{plugin(t)}}
}

func schema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("catalog.schema.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("https://hollislabs.com/plugins/catalog.schema.v2.json", v); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("https://hollislabs.com/plugins/catalog.schema.v2.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGeneratedCatalogConformsToSchema(t *testing.T) {
	d := document(t)
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if err := schema(t).Validate(v); err != nil {
		t.Fatal(err)
	}
	got, err := catalog.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Plugins[0]
	if p.ID != "hollis.bookmarks" || len(p.Hosts) != 2 || len(p.Summary.Capabilities) != 1 || len(p.Summary.Operations) != 2 {
		t.Fatalf("lost declaration: %+v", p)
	}
	if strings.Join(p.Summary.Effects, ",") != "destructive,read" {
		t.Fatalf("effects: %v", p.Summary.Effects)
	}
	digest := sha256.Sum256(manifestBytes(t))
	if p.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("manifest digest is not exact byte digest")
	}
}

func TestSchemaAndDecoderRejectLegacyAndIncompleteEntries(t *testing.T) {
	s := schema(t)
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"old schema", func(d map[string]any) { d["schema_version"] = 1 }},
		{"tier", func(d map[string]any) { first(d)["tier"] = "core" }},
		{"builtin source", func(d map[string]any) { first(d)["source"].(map[string]any)["type"] = "builtin" }},
		{"signature URL", func(d map[string]any) { archive(d)["sig_url"] = "https://example.org/file.sig" }},
		{"missing archive size", func(d map[string]any) { delete(archive(d), "size") }},
		{"missing checksum", func(d map[string]any) { delete(archive(d), "sha256") }},
		{"no archives", func(d map[string]any) { first(d)["archives"] = []any{} }},
		{"no hosts", func(d map[string]any) { first(d)["hosts"] = map[string]any{} }},
		{"missing manifest digest", func(d map[string]any) { delete(first(d), "manifest_sha256") }},
		{"HTTP archive", func(d map[string]any) { archive(d)["url"] = "http://example.org/file" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(document(t))
			var d map[string]any
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			tc.mutate(d)
			if err := s.Validate(d); err == nil {
				t.Fatal("schema accepted invalid entry")
			}
			raw, _ = json.Marshal(d)
			if _, err := catalog.Decode(raw); err == nil {
				t.Fatal("decoder accepted invalid entry")
			}
		})
	}
}

func first(d map[string]any) map[string]any { return d["plugins"].([]any)[0].(map[string]any) }
func archive(d map[string]any) map[string]any {
	return first(d)["archives"].([]any)[0].(map[string]any)
}

func TestIdentityPlatformAndSummaryChecks(t *testing.T) {
	d := document(t)
	d.Plugins = append(d.Plugins, d.Plugins[0])
	if d.Validate() == nil {
		t.Fatal("duplicate plugin accepted")
	}
	p := plugin(t)
	p.Archives = append(p.Archives, p.Archives[0])
	if p.Validate() == nil {
		t.Fatal("duplicate platform accepted")
	}
	raw := manifestBytes(t)
	raw = bytes.Replace(raw, []byte(`"effect": "destructive"`), []byte(`"effect": ""`), 1)
	if _, err := catalog.FromManifest(raw, plugin(t).Source, plugin(t).Archives, "", catalog.Directory{Status: "active"}); err == nil {
		t.Fatal("undeclared connector effect displayed as benign")
	}
}
