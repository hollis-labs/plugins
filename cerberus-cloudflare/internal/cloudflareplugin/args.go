package cloudflareplugin

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
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
	// Coded, so the host reports the caller's mistake as invalid_args, the
	// same code the digitalocean plugin uses for its argument refusals.
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, fmt.Errorf("invalid arguments: %s", strings.Join(a.problems, "; ")))
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

func (a *argMap) strOr(key, fallback string) string {
	if v := a.str(key); v != "" {
		return v
	}
	return fallback
}

func (a *argMap) required(key string) string {
	v := a.str(key)
	if v == "" {
		a.problems = append(a.problems, key+" is required")
	}
	return v
}

// intPtr reads an optional non-negative whole number. Absent is nil, not zero:
// a missing MX priority must not be sent as priority 0.
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

func (a *argMap) intOr(key string, fallback int) int {
	if v := a.intPtr(key); v != nil {
		return *v
	}
	return fallback
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
