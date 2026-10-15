package namecheapplugin

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// AGENTS.md in the Cerberus repo: "if an error message carries a recovery
// instruction, add a test that it survives redact.Text intact."
//
// A plugin cannot import internal/redact, so this mirrors the host's rules
// rather than running them. The first three are the shapes the kubernetes
// plugin guards against. The last two are copies of the host's own assignment
// and flag patterns (internal/redact/redact.go at v0.4.0-beta.2), because the
// guidance here names a secret and an environment variable, which is exactly
// what those two rewrite. If the host's redactor changes this does not follow:
// it is a floor, not a proof.
var redactionHazards = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"bearer", regexp.MustCompile(`(?i)\bbearer[ \t]+\S+`)},
	{"name=value", regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]*=\S+`)},
	{"flag word", regexp.MustCompile(`(?:^|\s)--?[A-Za-z][A-Za-z0-9-]*[ \t]+[A-Za-z]\S*`)},
	{"host assignment", regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|passcode|private[_-]?key|credentials?|authorization|cookie)[a-z0-9_.-]*["']?\s*(?:=>|=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&<>]+)`)},
	{"host flag", regexp.MustCompile(`(?i)((?:^|[\s"'` + "`" + `([{])--?[a-z0-9_-]*(?:api[_-]?key|token|secret|password|passwd|private[_-]?key|credentials?)[a-z0-9_-]*(?:=|[ \t]+))("[^"\n]*"|'[^'\n]*'|[^\s,;<>]+)`)},
}

func assertSurvivesRedaction(t *testing.T, label, text string) {
	t.Helper()
	for _, hazard := range redactionHazards {
		if match := hazard.pattern.FindString(text); match != "" {
			t.Errorf("%s contains a %s-shaped construct %q, which the host redactor rewrites\n  full text: %s",
				label, hazard.name, strings.TrimSpace(match), text)
		}
	}
}

// The missing-credential guidance is the string an operator most needs intact:
// it is the only place they learn every way to supply the credentials.
func TestMissingCredentialGuidanceSurvivesRedaction(t *testing.T) {
	assertSurvivesRedaction(t, "missing credential", errMissingCredential.Error())
	msg := credentialGuidance
	assertSurvivesRedaction(t, "credential guidance", msg)
	for _, want := range []string{envVar(SecretAPIKey), envVar(SecretAPIUser), envVar(SecretUsername), "connector-secrets.yaml", "keychain://namecheap/", "managed load namecheap"} {
		if !strings.Contains(msg, want) {
			t.Errorf("guidance lost %q: %s", want, msg)
		}
	}
}

// Every refusal that tells the caller what to do instead must arrive intact.
func TestRefusalsSurviveRedaction(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{set: sampleSet()})
	for op := range writeOperations {
		args := validArgs(op)
		delete(args, argAcknowledged)
		_, err := callTool(p, op, args)
		if err == nil {
			t.Fatalf("%s: want an acknowledgment refusal", op)
		}
		assertSurvivesRedaction(t, op+" acknowledgment refusal", err.Error())
	}
	result, err := callTool(p, "list_domains", map[string]any{argDryRun: true})
	_, message := failure(t, result, err)
	assertSurvivesRedaction(t, "dry-run refusal", message)

	assertSurvivesRedaction(t, "per-record refusal", errPerRecordWrite.Error())
	assertSurvivesRedaction(t, "set warning", setHostsWarning)
	assertSurvivesRedaction(t, "nameserver warning", nameserverWarning)

	refused := MCPAddressRefusal(t)
	assertSurvivesRedaction(t, "allow-list guidance", refused)

	status, _ := (&Plugin{}).Health(context.Background())
	assertSurvivesRedaction(t, "health", status.Message)
}

// MCPAddressRefusal is the message a caller gets when Namecheap refuses the
// request address.
func MCPAddressRefusal(t *testing.T) string {
	t.Helper()
	backend := &fakeBackend{err: &APIError{Action: "namecheap list domains", Numbers: []string{"1011150"}, Message: "Invalid request IP"}}
	result, err := callTool(loadedPlugin(t, backend), "list_domains", nil)
	_, message := failure(t, result, err)
	return message
}
