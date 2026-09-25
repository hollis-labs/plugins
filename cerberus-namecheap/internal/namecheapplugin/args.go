package namecheapplugin

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Argument keys the host adds to a call itself, from --dry-run and --ack. They
// are not in any operation's input schema because the caller does not set them
// as operation arguments; the host does.
const (
	argDryRun       = "dry_run"
	argAcknowledged = "acknowledged"
)

// argMap reads operation arguments and collects every problem, so a caller
// with three missing arguments hears about all three at once.
//
// Values arrive in two shapes. Over MCP and the API they are JSON, so numbers
// are float64 and booleans are bool. From `--arg key=value` on the CLI every
// value is a string. Both are accepted.
type argMap struct {
	raw      map[string]any
	problems []string
}

func newArgMap(raw map[string]any) *argMap {
	if raw == nil {
		raw = map[string]any{}
	}
	return &argMap{raw: raw}
}

func (a *argMap) err() error {
	if len(a.problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid arguments: %s", strings.Join(a.problems, "; "))
}

func (a *argMap) str(key string) string {
	switch v := a.raw[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func (a *argMap) required(key string) string {
	v := a.str(key)
	if v == "" {
		a.problems = append(a.problems, key+" is required")
	}
	return v
}

// intPtr reads an optional non-negative whole number. Absent is nil, not zero:
// an absent value must not be read as 0.
func (a *argMap) intPtr(key string) *int {
	v, ok := a.raw[key]
	if !ok || v == nil {
		return nil
	}
	var n int
	switch value := v.(type) {
	case float64:
		if value != math.Trunc(value) {
			a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number, got %v", key, value))
			return nil
		}
		n = int(value)
	case int:
		n = value
	case int64:
		n = int(value)
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number, got %q", key, value))
			return nil
		}
		n = parsed
	default:
		a.problems = append(a.problems, fmt.Sprintf("%s must be a whole number", key))
		return nil
	}
	if n < 0 {
		a.problems = append(a.problems, fmt.Sprintf("%s must not be negative, got %d", key, n))
		return nil
	}
	return &n
}

func (a *argMap) boolean(key string) bool {
	switch v := a.raw[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "false", "0", "no":
			return false
		case "true", "1", "yes":
			return true
		default:
			a.problems = append(a.problems, fmt.Sprintf("%s must be true or false, got %q", key, v))
			return false
		}
	case nil:
		return false
	default:
		a.problems = append(a.problems, key+" must be true or false")
		return false
	}
}

// strSlice reads an optional list of strings. JSON callers send an array; a
// lone string is taken as a one-item list.
func (a *argMap) strSlice(key string) []string {
	var out []string
	switch v := a.raw[key].(type) {
	case nil:
		return nil
	case string:
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	case []string:
		for _, s := range v {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				a.problems = append(a.problems, key+" entries must be strings")
				return nil
			}
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	default:
		a.problems = append(a.problems, key+" must be a list of strings")
		return nil
	}
	return out
}

// records reads set_dns_record_set's records. The array must be present:
// an absent array is not an empty zone, and treating it as one would delete
// every host. Each record needs type, host and value, the same rule the
// compiled-in connector applied.
func (a *argMap) records() []DNSRecord {
	raw, ok := a.raw["records"]
	if !ok || raw == nil {
		a.problems = append(a.problems, "records must explicitly contain the complete authoritative host record array")
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		a.problems = append(a.problems, "records must be an array of objects")
		return nil
	}
	out := make([]DNSRecord, 0, len(list))
	for i, item := range list {
		fields, ok := item.(map[string]any)
		if !ok {
			a.problems = append(a.problems, fmt.Sprintf("records[%d] must be an object", i))
			continue
		}
		rec := newArgMap(fields)
		record := DNSRecord{Type: rec.str("type"), Host: rec.str("host"), Value: rec.str("value")}
		if ttl := rec.intPtr("ttl"); ttl != nil {
			record.TTL = *ttl
		}
		if pref := rec.intPtr("mx_pref"); pref != nil {
			record.MXPref = *pref
		}
		for _, problem := range rec.problems {
			a.problems = append(a.problems, fmt.Sprintf("records[%d]: %s", i, problem))
		}
		if record.Type == "" || record.Host == "" || record.Value == "" {
			a.problems = append(a.problems, fmt.Sprintf("records[%d] requires type, host and value", i))
			continue
		}
		out = append(out, record)
	}
	return out
}
