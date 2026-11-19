package vastaiplugin

import (
	"encoding/json"
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

// has reports whether the caller supplied the argument at all, for a boolean
// whose default is true and so cannot treat absent as false.
func (a *argMap) has(key string) bool {
	v, ok := a.raw[key]
	return ok && v != nil && !(isString(v) && strings.TrimSpace(v.(string)) == "")
}

func isString(v any) bool { _, ok := v.(string); return ok }

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

// positiveID reads a required positive whole-number id. Zero is refused with
// the rest: no Vast.ai instance or offer has id 0, and a default of 0 would
// turn a missing argument into a call against nothing.
func (a *argMap) positiveID(key string) int {
	if _, ok := a.raw[key]; !ok || a.str(key) == "" {
		a.problems = append(a.problems, key+" is required")
		return 0
	}
	id := a.intPtr(key)
	if id == nil {
		return 0
	}
	if *id == 0 {
		a.problems = append(a.problems, key+" must be a positive whole number")
		return 0
	}
	return *id
}

// instanceID reads the required instance id.
func (a *argMap) instanceID() int { return a.positiveID("instance_id") }

// floatPtr reads an optional non-negative number. Absent is nil, not zero: a
// price ceiling of 0 must not be what a missing argument means.
func (a *argMap) floatPtr(key string) *float64 {
	v, ok := a.raw[key]
	if !ok || v == nil {
		return nil
	}
	var f float64
	switch value := v.(type) {
	case float64:
		f = value
	case int:
		f = float64(value)
	case int64:
		f = float64(value)
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			a.problems = append(a.problems, fmt.Sprintf("%s must be a number, got %q", key, value))
			return nil
		}
		f = parsed
	default:
		a.problems = append(a.problems, key+" must be a number")
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		a.problems = append(a.problems, fmt.Sprintf("%s must be a non-negative number", key))
		return nil
	}
	return &f
}

// stringMap reads an optional map of string to string. From the CLI a map
// arrives as JSON text.
func (a *argMap) stringMap(key string) map[string]string {
	var out map[string]string
	switch v := a.raw[key].(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			a.problems = append(a.problems, key+" must be a JSON object of strings")
			return nil
		}
	case map[string]string:
		out = v
	case map[string]any:
		out = map[string]string{}
		for k, item := range v {
			switch t := item.(type) {
			case string:
				out[k] = t
			case float64, bool, int:
				out[k] = fmt.Sprint(t)
			default:
				a.problems = append(a.problems, key+" values must be strings")
				return nil
			}
		}
	default:
		a.problems = append(a.problems, key+" must be an object of strings")
		return nil
	}
	return out
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

// rawString reads a string argument verbatim. user_data is a script, so
// leading and trailing whitespace is part of it and of its hash.
func (a *argMap) rawString(key string) string {
	switch v := a.raw[key].(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		a.problems = append(a.problems, key+" must be a string")
		return ""
	}
}
