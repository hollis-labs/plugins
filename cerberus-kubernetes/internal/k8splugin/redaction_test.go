package k8splugin

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
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

	// The code travels as a code (pkg/plugin ErrorResult), not as a prefix in
	// the text, and the host renders "<connector> <operation>:
	// credential_missing: <message>" from it.
	if code := codeOf(err); code != cerbplugin.ErrorCredentialMissing {
		t.Errorf("code = %q, want credential_missing: %q", code, err.Error())
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

// hostSensitiveKey mirrors SensitiveKey in the host's internal/redact: the host
// replaces the value of any JSON key it matches with [REDACTED], on output as
// well as on errors. A DTO field whose key matches arrives at the operator as
// [REDACTED] however harmless its value — which is how an ingress's TLS secret
// *name* was lost, found against a real cluster rather than by any test.
func hostSensitiveKey(key string) bool {
	key = strings.ToUpper(strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.TrimSpace(key)))
	for _, marker := range []string{"APIKEY", "APITOKEN", "ACCESSTOKEN", "AUTHTOKEN", "SECRET", "PASSWORD", "PASSWD", "PASSCODE", "PRIVATEKEY", "CREDENTIAL", "AUTHORIZATION", "COOKIE"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return key == "TOKEN" || strings.HasSuffix(key, "TOKEN")
}

// dtoKeysTheHostMayRedact are keys allowed to match anyway. It is empty, and
// should stay so: the host does not stop at the matched key, it hides every
// string and number beneath it, so a matching key on an object blanks the whole
// object. That is what happened to check_access's credential_plugin. An entry
// here needs an argument that nothing beneath the key is worth reading.
var dtoKeysTheHostMayRedact = map[string]string{}

func TestNoDTOKeyIsOneTheHostRedacts(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.Anonymous && name == "" {
				walk(field.Type, path)
				continue
			}
			if name == "" || name == "-" {
				continue
			}
			if hostSensitiveKey(name) {
				if _, allowed := dtoKeysTheHostMayRedact[name]; !allowed {
					t.Errorf("%s.%s: JSON key %q matches the host's SensitiveKey, so its value reaches the operator as [REDACTED]", path, field.Name, name)
				}
			}
			walk(field.Type, path+"."+field.Name)
		}
	}
	for _, dto := range []any{
		Health{}, Context{}, AccessCheck{}, Namespace{}, Node{}, Pod{}, Workload{}, Event{}, LogSnapshot{},
		WorkloadDetail{}, Service{}, Ingress{}, APIResourceList{}, Usage{}, Change{},
		List[Pod]{},
	} {
		walk(reflect.TypeOf(dto), reflect.TypeOf(dto).Name())
	}
}

func TestHostSensitiveKeyMirrorCatchesTheKeyThatWasLost(t *testing.T) {
	for _, key := range []string{"secret_name", "credential_plugin"} {
		if !hostSensitiveKey(key) {
			t.Fatalf("the mirror does not match %s, a key the host redacts; it would pass the bug it exists for", key)
		}
	}
}

// The exact text client-go produced against a real cluster for an
// interactiveMode Always helper. Passed through whole, the host redactor read
// "credentials:" as a key and ate the next word.
func TestExecFailureFromClientGoSurvivesRedactionAndNamesTheRightFix(t *testing.T) {
	live := errors.New(`Get "https://127.0.0.1:50639/version?timeout=30s": getting credentials: exec plugin cannot support interactive mode: standard input is not a terminal`)
	got := describeError(live, "https://127.0.0.1:50639")
	assertSurvivesRedaction(t, "interactive exec failure", got)
	if strings.Contains(got, "credentials:") {
		t.Errorf("message still carries the phrase the host redactor eats: %q", got)
	}
	if !strings.Contains(got, "IfAvailable") || strings.Contains(got, "PATH") {
		t.Errorf("message points at the wrong fix: %q", got)
	}

	missing := errors.New(`Get "https://api.example.com/version": getting credentials: exec: executable kubelogin not found`)
	got = describeError(missing, "https://api.example.com")
	assertSurvivesRedaction(t, "missing exec helper", got)
	if !strings.Contains(got, "executable kubelogin not found") || !strings.Contains(got, "check_access") {
		t.Errorf("missing-helper message lost its cause or recovery: %q", got)
	}
}
