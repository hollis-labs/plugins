package dataplugin

import (
	"encoding/json"
	"strings"
	"testing"
)

func projection() Projection {
	return Projection{Name: "documents", Schema: "published", Table: "documents", Columns: []Column{{"id", "int4"}, {"body", "text"}}, Filters: []Filter{{"tenant", "tenant", "text"}}}
}
func TestProjectionParametersRemainValues(t *testing.T) {
	p := projection()
	attack := "'; DROP TABLE documents; --"
	b, _ := json.Marshal(map[string]any{"tenant": attack})
	sql, args, err := p.compile(b, 10, "")
	if err != nil || strings.Contains(sql, attack) || args[0] != attack || !strings.Contains(sql, "OPERATOR(pg_catalog.=)") {
		t.Fatal("parameter became SQL or failed valid binding")
	}
}
func TestProjectionRejectsHiddenSQLWrongTypesAndUnknownParameters(t *testing.T) {
	p := projection()
	for _, raw := range []string{`{"tenant":"a","sql":"SELECT 1"}`, `{"tenant":1}`, `{"tenant":null}`, `{}`, `[]`} {
		if _, _, err := p.compile(json.RawMessage(raw), 100, ""); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	p.Table = "documents;DROP"
	if p.Validate() == nil {
		t.Fatal("identifier injection accepted")
	}
}
func TestVectorBoundsAndVersionBinding(t *testing.T) {
	p := projection()
	p.Vector = &Vector{Column: "embedding", Dimensions: 3}
	for _, raw := range []string{`{"tenant":"a","vector":[1,0],"top_k":1}`, `{"tenant":"a","vector":[0,0,0],"top_k":1}`, `{"tenant":"a","vector":[1,0,0],"top_k":101}`, `{"tenant":"a","vector":[1e100,0,0],"top_k":1}`} {
		if _, _, err := p.compile(json.RawMessage(raw), 100, "extensions"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	old := p.Version()
	p.Columns = p.Columns[:1]
	if old == p.Version() {
		t.Fatal("definition change retained grant version")
	}
}
