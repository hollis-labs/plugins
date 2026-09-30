package ghplugin

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// argDryRun is the key the host adds to a call itself, from --dry-run. It is
// not in any operation's input schema because the caller does not set it as
// an operation argument; the host does.
const argDryRun = "dry_run"

// GitHub's own grammar for the two path segments. Holding owner and repo to it
// keeps a caller's value from turning into a different API path ("..", a
// slash, a query string): url.PathEscape alone would still let ".." through.
var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// argMap reads operation arguments and collects every problem, so a caller
// with two missing arguments hears about both at once.
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

// ownerRepo reads the repository every operation targets.
func (a *argMap) ownerRepo() (string, string) {
	owner, repo := a.str("owner"), a.str("repo")
	switch {
	case owner == "":
		a.problems = append(a.problems, "owner is required")
	case !ownerPattern.MatchString(owner):
		a.problems = append(a.problems, fmt.Sprintf("owner %q is not a GitHub user or organization name", owner))
	}
	switch {
	case repo == "":
		a.problems = append(a.problems, "repo is required")
	case repo == "." || repo == ".." || !repoPattern.MatchString(repo):
		a.problems = append(a.problems, fmt.Sprintf("repo %q is not a GitHub repository name", repo))
	}
	return owner, repo
}

// limit reads the optional record limit, defaulting to defaultLimit and
// refusing anything outside 1..maxLimit rather than quietly clamping it.
func (a *argMap) limit() int {
	v, ok := a.raw["limit"]
	if !ok || v == nil {
		return defaultLimit
	}
	var n int
	switch value := v.(type) {
	case float64:
		if value != math.Trunc(value) {
			a.problems = append(a.problems, fmt.Sprintf("limit must be a whole number, got %v", value))
			return 0
		}
		n = int(value)
	case int:
		n = value
	case int64:
		n = int(value)
	case string:
		if strings.TrimSpace(value) == "" {
			return defaultLimit
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			a.problems = append(a.problems, fmt.Sprintf("limit must be a whole number, got %q", value))
			return 0
		}
		n = parsed
	default:
		a.problems = append(a.problems, "limit must be a whole number")
		return 0
	}
	if n < 1 || n > maxLimit {
		a.problems = append(a.problems, fmt.Sprintf("limit must be between 1 and %d, got %d", maxLimit, n))
		return 0
	}
	return n
}

func (a *argMap) boolean(key string) bool {
	switch v := a.raw[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true
		}
	}
	return false
}
