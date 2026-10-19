// Package dataplugin executes only operator-published, typed base-table projections.
package dataplugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

var refused = errors.New("data query refused; check the published projection and current host authorization")
var name = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type Filter struct {
	Parameter string `json:"parameter"`
	Column    string `json:"column"`
	Type      string `json:"type"`
}
type Vector struct {
	Column     string `json:"column"`
	Dimensions int    `json:"dimensions"`
}
type Projection struct {
	Name    string   `json:"name"`
	Schema  string   `json:"schema"`
	Table   string   `json:"table"`
	Columns []Column `json:"columns"`
	Filters []Filter `json:"filters"`
	Vector  *Vector  `json:"vector,omitempty"`
}

func (p Projection) Version() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validType(t string) bool {
	return t == "text" || t == "int4" || t == "int8" || t == "bool" || t == "float8"
}
func (p Projection) Validate() error {
	if !name.MatchString(p.Name) || !name.MatchString(p.Schema) || !name.MatchString(p.Table) || len(p.Columns) == 0 || len(p.Columns) > 32 || len(p.Filters) > 16 {
		return refused
	}
	cols := map[string]bool{}
	params := map[string]bool{}
	for _, c := range p.Columns {
		if !name.MatchString(c.Name) || !validType(c.Type) || cols[c.Name] {
			return refused
		}
		cols[c.Name] = true
	}
	for _, f := range p.Filters {
		if !name.MatchString(f.Column) || !name.MatchString(f.Parameter) || !validType(f.Type) || params[f.Parameter] || f.Parameter == "vector" || f.Parameter == "top_k" {
			return refused
		}
		params[f.Parameter] = true
	}
	if p.Vector != nil && (!name.MatchString(p.Vector.Column) || p.Vector.Dimensions < 1 || p.Vector.Dimensions > 2000) {
		return refused
	}
	return nil
}
func ident(s string) string { return pgx.Identifier{s}.Sanitize() }
func typed(raw json.RawMessage, kind string) (any, error) {
	if bytes.Equal(raw, []byte("null")) {
		return nil, refused
	}
	switch kind {
	case "text":
		var v string
		if json.Unmarshal(raw, &v) != nil || len(v) > 8192 {
			return nil, refused
		}
		return v, nil
	case "bool":
		var v bool
		if json.Unmarshal(raw, &v) != nil {
			return nil, refused
		}
		return v, nil
	case "int4", "int8":
		var v int64
		if json.Unmarshal(raw, &v) != nil || kind == "int4" && (v < math.MinInt32 || v > math.MaxInt32) {
			return nil, refused
		}
		return v, nil
	case "float8":
		var v float64
		if json.Unmarshal(raw, &v) != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, refused
		}
		return v, nil
	}
	return nil, refused
}

// compile never accepts SQL, identifiers or operator names from a caller.
// Vector's schema is discovered from the installed extension, not search_path.
func (p Projection) compile(raw json.RawMessage, maxRows int, vectorSchema string) (string, []any, error) {
	if p.Validate() != nil || maxRows < 1 || maxRows > 100 {
		return "", nil, refused
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(raw, &params) != nil || params == nil {
		return "", nil, refused
	}
	expected := len(p.Filters)
	if p.Vector != nil {
		expected += 2
	}
	if len(params) != expected {
		return "", nil, refused
	}
	// Bound raw cell transfer before pgx allocates row values. Text's worst
	// JSON escaping expansion is sixfold; fixed scalar columns need at most 64.
	sizes := []string{}
	for _, c := range p.Columns {
		if c.Type == "text" {
			sizes = append(sizes, "coalesce(pg_catalog.octet_length("+ident(c.Name)+"),0)::bigint*6")
		} else {
			sizes = append(sizes, "64")
		}
	}
	fits := "(" + strings.Join(sizes, "+") + " <= 1048000)"
	cols := []string{}
	for _, c := range p.Columns {
		cols = append(cols, "CASE WHEN "+fits+" THEN "+ident(c.Name)+" END")
	}
	cols = append(cols, fits)
	sql := "SELECT " + strings.Join(cols, ",") + " FROM ONLY " + pgx.Identifier{p.Schema, p.Table}.Sanitize()
	args := []any{}
	where := []string{}
	for _, f := range p.Filters {
		v, e := typed(params[f.Parameter], f.Type)
		if e != nil {
			return "", nil, e
		}
		args = append(args, v)
		where = append(where, fmt.Sprintf("%s OPERATOR(pg_catalog.=) $%d::pg_catalog.%s", ident(f.Column), len(args), f.Type))
	}
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	limit := maxRows + 1
	if p.Vector != nil {
		if !name.MatchString(vectorSchema) {
			return "", nil, refused
		}
		var v []float64
		var k int
		if json.Unmarshal(params["vector"], &v) != nil || len(v) != p.Vector.Dimensions || json.Unmarshal(params["top_k"], &k) != nil || k < 1 || k > maxRows {
			return "", nil, refused
		}
		parts := make([]string, len(v))
		norm := float64(0)
		for i, n := range v {
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > math.MaxFloat32 {
				return "", nil, refused
			}
			norm += n * n
			parts[i] = strconv.FormatFloat(n, 'g', -1, 64)
		}
		if norm == 0 {
			return "", nil, refused
		}
		args = append(args, "["+strings.Join(parts, ",")+"]")
		sql += fmt.Sprintf(" ORDER BY %s OPERATOR(%s.<=>) $%d::%s.vector", ident(p.Vector.Column), ident(vectorSchema), len(args), ident(vectorSchema))
		limit = k
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" LIMIT $%d", len(args))
	return sql, args, nil
}
func (p Projection) requiredColumns() []Column {
	m := map[string]string{}
	for _, c := range p.Columns {
		m[c.Name] = c.Type
	}
	for _, f := range p.Filters {
		if old, ok := m[f.Column]; ok && old != f.Type {
			return nil
		}
		m[f.Column] = f.Type
	}
	if p.Vector != nil {
		if _, ok := m[p.Vector.Column]; ok {
			return nil
		}
		m[p.Vector.Column] = "vector"
	}
	names := []string{}
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []Column{}
	for _, n := range names {
		out = append(out, Column{n, m[n]})
	}
	return out
}
