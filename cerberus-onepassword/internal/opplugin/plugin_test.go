package opplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/connector/conformance"
	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
	"gopkg.in/yaml.v3"
)

const resolvedSentinel = "pX7qR2vK9mWz4tLb" //nolint:gosec // a test sentinel, not a credential

// fakeToken is a well-formed service account token for a sign-in address.
// Its key material is placeholder: it is shaped so the SDK's core accepts it
// and goes to the network, which in these tests is a transport that refuses.
func fakeToken(address string) string {
	payload, _ := json.Marshal(map[string]any{
		"signInAddress": address,
		"userAuth":      map[string]any{"method": "SRPg-4096", "alg": "PBES2g-HS256", "iterations": 100000, "salt": "c2FsdHNhbHRzYWx0c2FsdA"},
		"email":         "service-account@example.com",
		"srpX":          "0123456789abcdef",
		"muk":           map[string]any{"alg": "A256GCM", "ext": true, "k": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "key_ops": []string{"encrypt", "decrypt"}, "kty": "oct", "kid": "mp"},
		"secretKey":     "A3-ABCDEF-ABCDEF-ABCDE-ABCDE-ABCDE-ABCDE",
		"deviceUuid":    "abcdefghijklmnopqrstuvwxyz",
	})
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

type fakeResolver struct {
	values map[string]string
	err    error
	calls  int
}

func (f *fakeResolver) Resolve(_ context.Context, ref string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.values[ref], nil
}

func loaded(t *testing.T, newClient func(context.Context, string) (resolver, error), token string) *Plugin {
	t.Helper()
	t.Setenv(envVar(SecretServiceAccountToken), "")
	p := newPlugin(newClient)
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretServiceAccountToken: token}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func withResolver(r resolver) func(context.Context, string) (resolver, error) {
	return func(context.Context, string) (resolver, error) { return r, nil }
}

func resolveOnce(t *testing.T, p *Plugin, ref string) subprocess.CommandResult {
	t.Helper()
	args, _ := json.Marshal(resolveArgs{Ref: ref})
	res, err := p.Command(context.Background(), subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	return res
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

// refusingTransport stands in for an unreachable 1Password.
type refusingTransport struct {
	mu    sync.Mutex
	hosts []string
}

func (r *refusingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.hosts = append(r.hosts, req.URL.Hostname())
	r.mu.Unlock()
	return nil, errors.New("dial tcp: connect: network is unreachable")
}

// The real SDK, offline: its WASM core signs in through http.DefaultClient,
// which here refuses every request. The resolve must come back as
// credential_missing, as one line with no WASM stack trace and no token, and
// the core must actually have tried the network.
//
// This is the unreachable half of "never serves a prior value". The other
// half, a success followed by an outage, needs a real sign-in, which a fake
// cannot provide; TestLiveResolve does it against real 1Password.
func TestAnUnreachable1PasswordIsCredentialMissing(t *testing.T) {
	saved := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = saved })
	refusing := &refusingTransport{}
	installHTTP(guardedTransport{next: refusing})

	token := fakeToken("example.1password.com")
	p := loaded(t, newClient, token)
	code, msg := payload(t, resolveOnce(t, p, "op://Deploy/Database/password"))
	if code != cerbplugin.ErrorCredentialMissing {
		t.Fatalf("code = %s: %s", code, msg)
	}
	if len(refusing.hosts) == 0 || refusing.hosts[0] != "example.1password.com" {
		t.Fatalf("the SDK did not go through the installed client: %v", refusing.hosts)
	}
	for _, leaked := range []string{"wasm stack trace", "\n", token, strings.TrimPrefix(token, tokenPrefix)[:24]} {
		if strings.Contains(msg, leaked) {
			t.Errorf("failure carries %q: %s", leaked, msg)
		}
	}
	if !strings.Contains(msg, "sign-in failed") {
		t.Errorf("failure does not say what failed: %s", msg)
	}
}

// Every resolve reaches 1Password; the plugin keeps no value between calls,
// and a failure drops the client so the next resolve signs in afresh.
func TestEveryResolveReachesOnePassword(t *testing.T) {
	ref := "op://Deploy/Database/password"
	r := &fakeResolver{values: map[string]string{ref: resolvedSentinel}}
	made := 0
	p := loaded(t, func(context.Context, string) (resolver, error) { made++; return r, nil }, fakeToken("example.1password.com"))
	if res := resolveOnce(t, p, ref); res.Content != resolvedSentinel {
		t.Fatalf("first = %+v", res)
	}
	r.err = errors.New("1password is down")
	if res := resolveOnce(t, p, ref); res.Action != "error" || strings.Contains(res.Content, resolvedSentinel) {
		t.Fatalf("a failed 1Password was answered: %+v", res)
	}
	r.err = nil
	resolveOnce(t, p, ref)
	if r.calls != 3 {
		t.Fatalf("1Password was asked %d times for three resolves", r.calls)
	}
	if made != 2 {
		t.Fatalf("clients made = %d, want 2: one, then a fresh one after the failure", made)
	}
}

// Load makes no network call and compiles nothing: the client is made at the
// first resolve.
func TestLoadIsOffline(t *testing.T) {
	made := 0
	loaded(t, func(context.Context, string) (resolver, error) { made++; return &fakeResolver{}, nil }, fakeToken("example.1password.com"))
	if made != 0 {
		t.Fatal("Load built a client")
	}
}

func TestTokenChecks(t *testing.T) {
	if address, err := signInAddress(fakeToken("my.1password.eu")); err != nil || address != "my.1password.eu" {
		t.Fatalf("valid token: %q %v", address, err)
	}
	cases := map[string]string{
		"empty":          "",
		"not ops":        "ghp_notatoken",
		"not base64":     "ops_%%%%",
		"no address":     tokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"x@example.com"}`)),
		"staging domain": fakeToken("team.b5dev.com"),
		"lookalike":      fakeToken("1password.com.example.net"),
	}
	for name, token := range cases {
		_, err := signInAddress(token)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		if token != "" && strings.Contains(err.Error(), strings.TrimPrefix(token, tokenPrefix)) {
			t.Errorf("%s: the token reached the error: %v", name, err)
		}
	}
}

// The transport goes only to production 1Password domains over HTTPS, and
// verifies certificates.
func TestTransportIsFencedToProduction1Password(t *testing.T) {
	var reached []string
	guard := guardedTransport{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		reached = append(reached, r.URL.String())
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})}
	for raw, allowed := range map[string]bool{
		"https://my.1password.com/api":     true,
		"https://my.1password.ca/api":      true,
		"https://team.b5dev.com/api":       false,
		"http://my.1password.com/api":      false,
		"https://1password.com.evil.test/": false,
		"https://evil1password.com/":       false,
	} {
		u, _ := url.Parse(raw)
		_, err := guard.RoundTrip(&http.Request{URL: u})
		if (err == nil) != allowed {
			t.Errorf("%s: allowed=%v, err=%v", raw, allowed, err)
		}
	}
	if len(reached) != 2 {
		t.Errorf("reached %v", reached)
	}
	inner, ok := newTransport().(guardedTransport).next.(*http.Transport)
	if !ok || inner.TLSClientConfig == nil || inner.TLSClientConfig.InsecureSkipVerify || inner.ResponseHeaderTimeout == 0 {
		t.Fatal("the transport does not verify certificates or has no timeout")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// New points the SDK's HTTP at this plugin's fenced, bounded client, and its
// logging off stdout, the protocol stream.
func TestNewInstallsTheClientAndLogDestination(t *testing.T) {
	saved := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = saved; log.SetOutput(os.Stderr) })
	log.SetOutput(os.Stdout)
	_ = New()
	if _, ok := http.DefaultClient.Transport.(guardedTransport); !ok || http.DefaultClient.Timeout == 0 {
		t.Fatalf("http.DefaultClient is not the fenced, bounded client: %+v", http.DefaultClient)
	}
	if log.Writer() == os.Stdout {
		t.Fatal("the standard logger, which the SDK uses, writes to stdout")
	}
}

// The SDK client can create and delete items, vaults, groups and
// environments, and can load the 1Password app's native library. None of
// that is reachable from this plugin's code.
func TestNoCodeReachesAWriteAPI(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{".Items()", ".ItemsAPI", ".Vaults()", ".VaultsAPI", ".Groups()", ".GroupsAPI", ".Environments()", ".EnvironmentsAPI", "WithDesktopAppIntegration"}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file) //nolint:gosec // this package's own source
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range banned {
			if bytes.Contains(src, []byte(b)) {
				t.Errorf("%s references %s", file, b)
			}
		}
	}
}

func TestResolveRefusals(t *testing.T) {
	p := loaded(t, withResolver(&fakeResolver{values: map[string]string{"op://v/blank/f": ""}}), fakeToken("example.1password.com"))
	cases := []struct {
		name, command, args string
		code                cerbplugin.ErrorCode
		want                string
	}{
		{"unknown command", "something/else", `{"ref":"op://v/i/f"}`, cerbplugin.ErrorInvalidArgs, "serves only " + ResolveCommand},
		{"bad args", ResolveCommand, `not json`, cerbplugin.ErrorInvalidArgs, "JSON object"},
		{"other scheme", ResolveCommand, `{"ref":"keeper://uid/field/password"}`, cerbplugin.ErrorInvalidArgs, "only op://"},
		{"empty value", ResolveCommand, `{"ref":"op://v/blank/f"}`, cerbplugin.ErrorCredentialMissing, "empty value"},
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
	}
}

func TestMissingTokenNamesTheRecovery(t *testing.T) {
	p := loaded(t, withResolver(&fakeResolver{}), "")
	code, msg := payload(t, resolveOnce(t, p, "op://v/i/f"))
	if code != cerbplugin.ErrorCredentialMissing || msg != credentialGuidance {
		t.Fatalf("got %s %q", code, msg)
	}
	if health, _ := p.Health(context.Background()); health.OK || health.Message != credentialGuidance {
		t.Fatalf("health = %+v", health)
	}
}

// Nothing a resolve failure says carries the token, a value resolved
// earlier, the reference, or the vault and item names in it, and a WASM
// stack trace is cut off.
func TestFailuresCarryNoValueTokenReferenceOrNames(t *testing.T) {
	good, bad := "op://Deploy/Database/password", "op://Jane Personal/Bank login/password"
	token := fakeToken("example.1password.com")
	r := &fakeResolver{values: map[string]string{good: resolvedSentinel}}
	p := loaded(t, withResolver(r), token)
	resolveOnce(t, p, good)
	r.err = errors.New("no item matched 'Bank login' in vault 'Jane Personal' for " + bad + " (prior " + resolvedSentinel + ", token " + token + ")\nwasm stack trace:\n\tcore.wasm.invoke")
	_, msg := payload(t, resolveOnce(t, p, bad))
	for _, leaked := range []string{resolvedSentinel, token, "Bank login", "Jane Personal", "wasm stack trace"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("failure carries %q: %s", leaked, msg)
		}
	}
	if !strings.Contains(msg, referenceMarker) {
		t.Errorf("failure lost the reference marker: %s", msg)
	}
}

func TestResolutionIsNotAnOperation(t *testing.T) {
	ops := Manifest().Operations
	if len(ops) != 1 || ops[0].Name != "status" {
		t.Fatalf("operations = %v, want status alone", ops)
	}
	p := loaded(t, withResolver(&fakeResolver{}), fakeToken("example.1password.com"))
	for _, tool := range []string{cerbplugin.ToolNameForOperation(ConnectorID, "resolve"), ResolveCommand} {
		if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool}); err == nil {
			t.Errorf("tool %q was served", tool)
		}
	}
}

func TestStatusReportsWithoutCredentialMaterial(t *testing.T) {
	r := &fakeResolver{}
	token := fakeToken("team.1password.com")
	p := loaded(t, withResolver(r), token)
	res, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: cerbplugin.ToolNameForOperation(ConnectorID, "status")})
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(res.Content, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.SignInAddress != "team.1password.com" || status.Scheme != Scheme {
		t.Fatalf("status = %+v", status)
	}
	for _, material := range []string{token, "0123456789abcdef", "A3-ABCDEF", "service-account@example.com"} {
		if strings.Contains(string(res.Content), material) {
			t.Errorf("status carries token material %q", material)
		}
	}
	if r.calls != 0 {
		t.Fatal("status called 1Password")
	}
}

// A mirror of the host redactor's rules, as the other plugins keep: a floor,
// not a proof.
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
	for label, text := range map[string]string{"guidance": credentialGuidance, "not a token": errNotAServiceAccountToken.Error()} {
		for _, hazard := range redactionHazards {
			if match := hazard.pattern.FindString(text); match != "" {
				t.Errorf("%s contains a %s-shaped construct %q", label, hazard.name, match)
			}
		}
	}
	for _, want := range []string{"onepassword/service_account_token", envVar(SecretServiceAccountToken), "managed load onepassword"} {
		if !strings.Contains(credentialGuidance, want) {
			t.Errorf("guidance lost %q", want)
		}
	}
}

func TestTheTokenIsTheOneCredential(t *testing.T) {
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != SecretServiceAccountToken || !secrets[0].Required || !secrets[0].IsCredential() {
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

// A resolve waiting behind a slow one gives up at its own deadline.
func TestAResolveHonoursItsDeadlineBehindASlowOne(t *testing.T) {
	slow := &blockingResolver{release: make(chan struct{}), started: make(chan struct{})}
	p := loaded(t, withResolver(slow), fakeToken("example.1password.com"))
	args, _ := json.Marshal(resolveArgs{Ref: "op://v/i/f"})
	first := make(chan subprocess.CommandResult, 1)
	go func() {
		res, _ := p.Command(context.Background(), subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
		first <- res
	}()
	<-slow.started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res, err := p.Command(ctx, subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
	close(slow.release)
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

type blockingResolver struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func (b *blockingResolver) Resolve(context.Context, string) (string, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return resolvedSentinel, nil
}

// plugin.yaml claims the op:// scheme, and the host accepts the claim.
func TestPluginYAMLClaimsTheScheme(t *testing.T) {
	spec := PluginYAML()
	b := spec.Cerberus.SecretBackend
	if b == nil || b.Scheme != "op" {
		t.Fatalf("secret_backend = %+v", b)
	}
	if problems := b.Validate(); len(problems) != 0 {
		t.Fatalf("the host refuses the claim: %v", problems)
	}
	if ResolveCommand != cerbplugin.ResolveCommand {
		t.Fatalf("the resolve command drifted from the host's: %q", ResolveCommand)
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "secret_backend:") || !strings.Contains(string(data), "scheme: op") {
		t.Fatalf("plugin.yaml does not carry the claim:\n%s", data)
	}
}
