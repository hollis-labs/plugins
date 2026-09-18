package k8splugin

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// AGENTS.md: "if an error message carries a recovery instruction, add a test
// that it survives redact.Text intact. A safety net that eats the instruction
// is worse than no instruction."
//
// A plugin cannot import internal/redact, so this mirrors the host's rules
// rather than running them. That makes it a guard on the *shape* of what this
// package emits: keep operator-facing strings clear of the constructs the
// redactor rewrites, and there is nothing for it to eat. Seven patches to those
// regexes is the repo's own evidence that avoiding the shapes beats trusting
// the exemptions.
//
// If the host's redactor changes, this test does not automatically follow. It
// is a floor, not a proof.
var redactionHazards = []struct {
	name    string
	pattern *regexp.Regexp
	why     string
}{
	{
		name:    "bearer",
		pattern: regexp.MustCompile(`(?i)\bbearer[ \t]+\S+`),
		why:     "the word following Bearer is rewritten; it ate its own guidance twice",
	},
	{
		name:    "assignment",
		pattern: regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]*=\S+`),
		why:     "NAME=value is read as a credential assignment and the value is swallowed",
	},
	{
		name:    "flag",
		pattern: regexp.MustCompile(`(?:^|\s)--?[A-Za-z][A-Za-z0-9-]*[ \t]+\S+`),
		why:     "the word after a flag is rewritten; this is how the recovery verb was eaten",
	},
}

func assertSurvivesRedaction(t *testing.T, label, text string) {
	t.Helper()
	if text == "" {
		return
	}
	for _, hazard := range redactionHazards {
		if match := hazard.pattern.FindString(text); match != "" {
			t.Errorf("%s contains a %s-shaped construct %q, which the host redactor rewrites (%s)\n  full text: %s",
				label, hazard.name, strings.TrimSpace(match), hazard.why, text)
		}
	}
}

// The credential_missing message is the single most important string this
// package produces: it is what an operator sees when the exec helper is not on
// the daemon's PATH, and it names both the binary and the escape hatch.
func TestCredentialMissingRecoverySurvivesRedaction(t *testing.T) {
	_, err := ResolveCredentialCommand("cerberus-test-missing-helper", "")
	if err == nil {
		t.Fatal("want an error")
	}
	assertSurvivesRedaction(t, "ResolveCredentialCommand", err.Error())

	// The error code prefix must survive as a code. The host exempts its own
	// error codes from the assignment rule, but only when they are recognised,
	// so keep the colon form the rest of Cerberus uses.
	if !strings.HasPrefix(err.Error(), "credential_missing:") {
		t.Errorf("error code prefix missing: %q", err.Error())
	}
}

func TestPreflightProblemsSurviveRedaction(t *testing.T) {
	path := writeKubeconfig(t, t.TempDir())

	for _, contextName := range []string{"prod", "nope", ""} {
		check, err := Preflight(ClusterOptions{Kubeconfig: path, Context: contextName})
		if err != nil {
			t.Fatalf("Preflight(%q): %v", contextName, err)
		}
		for _, problem := range check.Problems {
			assertSurvivesRedaction(t, "Preflight problem", problem)
		}
	}
}

func TestDescribeErrorRecoveriesSurviveRedaction(t *testing.T) {
	server := "https://api.prod.example.com:6443"
	for _, err := range []error{
		errors.New("an exec credential plugin failed"),
		errors.New("plain failure"),
	} {
		assertSurvivesRedaction(t, "describeError", describeError(err, server))
	}
}

// The health message reaches an operator through `connectors list`, which is
// where a down tunnel and a down cluster have to be distinguishable.
func TestRestConfigMisconfigurationMessageSurvivesRedaction(t *testing.T) {
	_, _, err := RestConfig(ClusterOptions{Token: "a-token"})
	if err == nil {
		t.Fatal("want an error")
	}
	assertSurvivesRedaction(t, "RestConfig", err.Error())
}

// A guard that has never failed is not yet a guard. This asserts each hazard
// pattern actually fires on the construct it is meant to catch — each string
// below is a real example from the AGENTS.md list of things redact.Text has
// eaten.
func TestRedactionHazardsDetectKnownBadStrings(t *testing.T) {
	cases := map[string]string{
		"bearer":     "authenticate with a Bearer JWT from the gateway",
		"assignment": "set CERBERUS_KUBE_TOKEN=your-token and retry",
		"flag":       "re-run with --context prod to select the cluster",
	}
	for name, text := range cases {
		var fired bool
		for _, hazard := range redactionHazards {
			if hazard.name == name && hazard.pattern.MatchString(text) {
				fired = true
			}
		}
		if !fired {
			t.Errorf("hazard %q did not match %q; the guard would pass a string the host rewrites", name, text)
		}
	}
}
