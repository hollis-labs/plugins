package forgeplugin

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

// positiveID reads a required id. Zero is refused with the rest: Forge ids
// start at 1, and a default of 0 would turn a missing argument into a call
// against nothing.
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

// serverSite reads the server and site ids most operations take.
func (a *argMap) serverSite() (int, int) {
	return a.positiveID("server_id"), a.positiveID("site_id")
}

// requiredRaw reads a required string verbatim: a deployment script's
// whitespace is part of it. A value that is only whitespace counts as absent.
func (a *argMap) requiredRaw(key string) string {
	v := a.rawString(key)
	if strings.TrimSpace(v) == "" && !a.hasProblem(key) {
		a.problems = append(a.problems, key+" is required")
	}
	return v
}

func (a *argMap) hasProblem(key string) bool {
	for _, p := range a.problems {
		if strings.HasPrefix(p, key+" ") {
			return true
		}
	}
	return false
}

// rawString reads a string argument verbatim. A deployment script's leading
// and trailing whitespace is part of it.
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
