package tailscaleplugin

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
// rather than running them: the host's assignment and flag patterns
// (internal/redact/redact.go), plus the bearer, name=value and flag-word shapes.
// If the host's redactor changes this does not follow: it is a floor, not a
// proof.
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
// it is the only place they learn every way to supply the credential.
func TestMissingCredentialGuidanceSurvivesRedaction(t *testing.T) {
	assertSurvivesRedaction(t, "missing credential", errMissingCredential.Error())
	assertSurvivesRedaction(t, "credential guidance", credentialGuidance)
	for _, want := range []string{APIKeyEnvVar, OAuthClientIDEnvVar, OAuthClientSecretEnvVar, "connector-secrets.yaml", "managed load tailscale"} {
		if !strings.Contains(credentialGuidance, want) {
			t.Errorf("guidance lost %q: %s", want, credentialGuidance)
		}
	}
}

func TestRefusalsSurviveRedaction(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for op := range ackOperations {
		args := validArgs(op)
		delete(args, argAcknowledged)
		_, err := callTool(p, op, args)
		if err == nil {
			t.Fatalf("%s: want an acknowledgment refusal", op)
		}
		assertSurvivesRedaction(t, op+" acknowledgment refusal", err.Error())
	}
	result, err := callTool(p, "list_devices", map[string]any{argDryRun: true})
	_, message := failure(t, result, err)
	assertSurvivesRedaction(t, "dry-run refusal", message)

	status, _ := unconfiguredPlugin(t).Health(context.Background())
	assertSurvivesRedaction(t, "health", status.Message)
}
