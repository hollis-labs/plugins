package keeperplugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/connector/conformance"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

// fakeVault answers from a map, or fails with err.
type fakeVault struct {
	values map[string][]string
	err    error
	calls  int
}

func (f *fakeVault) GetNotationResults(ref string) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.values[ref], nil
}

const validConfigJSON = `{"hostname":"keepersecurity.com","clientId":"Y2xpZW50LWlkLXNlbnRpbmVs","privateKey":"cHJpdmF0ZS1rZXktc2VudGluZWw=","appKey":"YXBwLWtleS1zZW50aW5lbA==","serverPublicKeyId":"10"}`

func loaded(t *testing.T, v vault, config string) *Plugin {
	t.Helper()
	p := newPlugin(func(map[string]string) (vault, error) { return v, nil })
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretKSMConfig: config}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func payload(t *testing.T, res subprocess.CommandResult) (cerbplugin.ErrorCode, string) {
	t.Helper()
	if res.Action != "error" {
		t.Fatalf("expected a failure, got %+v", res)
	}
	var body struct {
		Error struct {
			Code    cerbplugin.ErrorCode `json:"code"`
			Message string               `json:"message"`
		} `json:"cerberus_error"`
	}
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("failure content is not a coded payload: %v: %s", err, res.Content)
	}
	return body.Error.Code, body.Error.Message
}

func TestResolveReturnsTheValueAndNothingElse(t *testing.T) {
	ref := "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"
	p := loaded(t, &fakeVault{values: map[string][]string{ref: {resolvedSentinel}}}, validConfigJSON)
	res := resolveOnce(t, p, ref)
	if res.Action != "message" || res.Content != resolvedSentinel {
		t.Fatalf("resolve = %+v", res)
	}
}

// Every resolve reaches Keeper; the plugin keeps nothing between calls.
func TestEveryResolveReachesKeeper(t *testing.T) {
	ref := "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"
	v := &fakeVault{values: map[string][]string{ref: {resolvedSentinel}}}
	p := loaded(t, v, validConfigJSON)
	resolveOnce(t, p, ref)
	v.err = errors.New("keeper is down")
	if res := resolveOnce(t, p, ref); res.Action != "error" || strings.Contains(res.Content, resolvedSentinel) {
		t.Fatalf("a failed Keeper was answered: %+v", res)
	}
	if v.calls != 2 {
		t.Fatalf("Keeper was asked %d times for two resolves", v.calls)
	}
}

func TestResolveRefusals(t *testing.T) {
	p := loaded(t, &fakeVault{values: map[string][]string{
		"keeper://many/field/password": {"one-value-here", "two-value-here"},
		"keeper://none/field/password": nil,
		"keeper://blank/field/url":     {""},
	}}, validConfigJSON)
	cases := []struct {
		name, command, args string
		code                cerbplugin.ErrorCode
		want                string
	}{
		{"unknown command", "something/else", `{"ref":"keeper://x/field/password"}`, cerbplugin.ErrorInvalidArgs, "serves only " + ResolveCommand},
		{"bad args", ResolveCommand, `not json`, cerbplugin.ErrorInvalidArgs, "JSON object"},
		{"other scheme", ResolveCommand, `{"ref":"op://vault/item/field"}`, cerbplugin.ErrorInvalidArgs, "only keeper://"},
		{"several values", ResolveCommand, `{"ref":"keeper://many/field/password"}`, cerbplugin.ErrorCredentialMissing, "names 2 values"},
		{"no value", ResolveCommand, `{"ref":"keeper://none/field/password"}`, cerbplugin.ErrorCredentialMissing, "no value"},
		{"empty value", ResolveCommand, `{"ref":"keeper://blank/field/url"}`, cerbplugin.ErrorCredentialMissing, "empty value"},
	}
	for _, tc := range cases {
		res, err := p.Command(context.Background(), subprocess.CommandRequest{Name: tc.command, Args: tc.args})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		code, msg := payload(t, res)
		if code != tc.code || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, code, msg, tc.code, tc.want)
		}
		if strings.Contains(msg, "one-value-here") {
			t.Errorf("%s: a value reached the error: %s", tc.name, msg)
		}
	}
}

// A missing configuration is credential_missing with the recovery named.
func TestMissingConfigurationNamesTheRecovery(t *testing.T) {
	t.Setenv(envVar(SecretKSMConfig), "")
	p := loaded(t, nil, "")
	code, msg := payload(t, resolveOnce(t, p, "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"))
	if code != cerbplugin.ErrorCredentialMissing || msg != credentialGuidance {
		t.Fatalf("got %s %q", code, msg)
	}
	health, _ := p.Health(context.Background())
	if health.OK || health.Message != credentialGuidance {
		t.Fatalf("health = %+v", health)
	}
}

// An unbound configuration (a one-time token not yet bound) is refused: this
// plugin never binds, since binding consumes the token and writes a new
// credential.
func TestUnboundConfigurationIsRefused(t *testing.T) {
	unbound := `{"hostname":"keepersecurity.com","clientKey":"b25lLXRpbWUtdG9rZW4tc2VudGluZWw"}`
	called := false
	p := newPlugin(func(map[string]string) (vault, error) { called = true; return nil, nil })
	_, _ = p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretKSMConfig: unbound}})
	_, _ = p.Load(context.Background())
	if called {
		t.Fatal("an unbound configuration reached the SDK, which would try to bind it")
	}
	code, msg := payload(t, resolveOnce(t, p, "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"))
	if code != cerbplugin.ErrorCredentialMissing || !strings.Contains(msg, "not a bound Secrets Manager configuration") {
		t.Fatalf("got %s %q", code, msg)
	}
	if strings.Contains(msg, "b25lLXRpbWUtdG9rZW4tc2VudGluZWw") {
		t.Fatalf("the token reached the error: %s", msg)
	}
}

func TestParseConfigTakesBase64OrJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"json":   validConfigJSON,
		"base64": base64.StdEncoding.EncodeToString([]byte(validConfigJSON)),
	} {
		config, err := parseConfig(raw)
		if err != nil || config["hostname"] != "keepersecurity.com" {
			t.Errorf("%s: %v %v", name, config, err)
		}
	}
	for name, raw := range map[string]string{"garbage": "%%%not-a-config", "array": "[1,2]"} {
		_, err := parseConfig(raw)
		if err == nil {
			t.Errorf("%s parsed", name)
		} else if strings.Contains(err.Error(), raw) {
			t.Errorf("%s: the configuration text reached the error: %v", name, err)
		}
	}
}

// Nothing a resolve failure says carries a held value (the configuration's
// keys, or a value resolved earlier), the reference, or the record title in
// it.
func TestFailuresCarryNoValueReferenceOrTitle(t *testing.T) {
	good := "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"
	bad := "keeper://Payroll database for Jane/field/password"
	v := &fakeVault{values: map[string][]string{good: {resolvedSentinel}}}
	p := loaded(t, v, validConfigJSON)
	resolveOnce(t, p, good)
	v.err = errors.New("notation error - multiple records match record 'Payroll database for Jane' in " + bad +
		"; last value " + resolvedSentinel + "; key cHJpdmF0ZS1rZXktc2VudGluZWw=")
	_, msg := payload(t, resolveOnce(t, p, bad))
	for _, leaked := range []string{resolvedSentinel, "Payroll database for Jane", "cHJpdmF0ZS1rZXktc2VudGluZWw="} {
		if strings.Contains(msg, leaked) {
			t.Errorf("failure carries %q: %s", leaked, msg)
		}
	}
	if !strings.Contains(msg, referenceMarker) {
		t.Errorf("failure lost the reference marker: %s", msg)
	}
}

// Resolution is off every surface: the only connector operation is status,
// so the host generates no CLI command, API operation or MCP tool that
// resolves, and the plugin refuses any tool but status.
func TestResolutionIsNotAnOperation(t *testing.T) {
	ops := Manifest().Operations
	if len(ops) != 1 || ops[0].Name != "status" {
		t.Fatalf("operations = %v, want status alone", ops)
	}
	p := loaded(t, &fakeVault{}, validConfigJSON)
	for _, tool := range []string{
		cerbplugin.ToolNameForOperation(ConnectorID, "resolve"),
		cerbplugin.ToolNameForOperation(ConnectorID, ResolveCommand),
		ResolveCommand,
	} {
		if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool}); err == nil {
			t.Errorf("tool %q was served", tool)
		}
	}
}

// status is offline and carries no configuration beyond the region host.
func TestStatusReportsWithoutCredentialMaterial(t *testing.T) {
	v := &fakeVault{}
	p := loaded(t, v, validConfigJSON)
	res, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "status")})
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(res.Content, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Hostname != "keepersecurity.com" || status.Scheme != Scheme {
		t.Fatalf("status = %+v", status)
	}
	for _, secret := range []string{"Y2xpZW50LWlkLXNlbnRpbmVs", "cHJpdmF0ZS1rZXktc2VudGluZWw=", "YXBwLWtleS1zZW50aW5lbA=="} {
		if strings.Contains(string(res.Content), secret) {
			t.Errorf("status carries configuration material %q", secret)
		}
	}
	if v.calls != 0 {
		t.Fatal("status called Keeper")
	}
}

// AGENTS.md in the Cerberus repo: a recovery instruction survives redact.Text.
// A plugin cannot import internal/redact, so these mirror the host's rules, as
// the namecheap plugin's redaction_test.go does. A floor, not a proof.
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

func TestRecoveryInstructionsSurviveRedaction(t *testing.T) {
	for label, text := range map[string]string{
		"credential guidance": credentialGuidance,
		"unbound":             errUnbound.Error(),
	} {
		for _, hazard := range redactionHazards {
			if match := hazard.pattern.FindString(text); match != "" {
				t.Errorf("%s contains a %s-shaped construct %q, which the host redactor rewrites", label, hazard.name, match)
			}
		}
	}
	for _, want := range []string{"keeper/ksm_config", envVar(SecretKSMConfig), "managed load keeper", "outside Cerberus"} {
		if !strings.Contains(credentialGuidance, want) {
			t.Errorf("guidance lost %q", want)
		}
	}
}

func TestTheConfigurationIsTheOneCredential(t *testing.T) {
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != SecretKSMConfig || !secrets[0].Required || !secrets[0].IsCredential() {
		t.Fatalf("secrets = %+v", secrets)
	}
}

func TestManifestConforms(t *testing.T) {
	if problems := conformance.Definition(Definition()); len(problems) > 0 {
		t.Errorf("Definition does not conform:\n  %s", conformance.Report(problems))
	}
	data, err := yaml.Marshal(PluginYAML())
	if err != nil {
		t.Fatal(err)
	}
	var spec cerbplugin.PluginYAML
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if problems := conformance.Manifest(spec.Cerberus.Connector); len(problems) > 0 {
		t.Errorf("generated plugin.yaml does not conform:\n  %s", conformance.Report(problems))
	}
	if gaps := spec.Cerberus.Connector.ContractGaps(); len(gaps) != 0 {
		t.Errorf("contract gaps: %v", gaps)
	}
	if err := PluginYAML().Validate(t.TempDir()); err != nil {
		t.Error(err)
	}
	if spec.Cerberus.Host != (cerbplugin.HostRange{MinContract: 1, MaxContract: 1}) {
		t.Errorf("host range %+v", spec.Cerberus.Host)
	}
	for _, name := range spec.Cerberus.Surfaces.MCP {
		op, _ := cerbplugin.OperationFromToolName(ConnectorID, cerbplugin.ToolNameForOperation(ConnectorID, name), spec.Cerberus.Connector)
		if op.EffectiveEffect() != contract.EffectRead {
			t.Errorf("%s is suggested for MCP", name)
		}
	}
}

// A resolve waiting behind a slow one gives up at its own deadline rather
// than queueing forever.
func TestAResolveHonoursItsDeadlineBehindASlowOne(t *testing.T) {
	release := make(chan struct{})
	slow := &blockingVault{release: release, started: make(chan struct{})}
	p := loaded(t, slow, validConfigJSON)
	args, _ := json.Marshal(resolveArgs{Ref: "keeper://AbCdEfGhIjKlMnOpQrStUv/field/password"})
	first := make(chan subprocess.CommandResult, 1)
	go func() {
		res, _ := p.Command(context.Background(), subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
		first <- res
	}()
	<-slow.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := p.Command(ctx, subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := payload(t, res); code != cerbplugin.ErrorCredentialMissing {
		t.Fatalf("code = %s", code)
	}
	if got := <-first; got.Content != resolvedSentinel {
		t.Fatalf("the slow resolve = %+v", got)
	}
}

type blockingVault struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingVault) GetNotationResults(string) ([]string, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return []string{resolvedSentinel}, nil
}
