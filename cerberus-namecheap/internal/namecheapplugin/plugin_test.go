package namecheapplugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	domains []Domain
	status  *DomainStatus
	set     *DNSRecordSet
	err     error
	calls   []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) ListDomains(context.Context) ([]Domain, error) {
	f.record("ListDomains")
	return f.domains, f.err
}

func (f *fakeBackend) GetDomainStatus(_ context.Context, domain string) (*DomainStatus, error) {
	f.record("GetDomainStatus", domain)
	return f.status, f.err
}

func (f *fakeBackend) GetDNSRecordSet(_ context.Context, sld, tld string) (*DNSRecordSet, error) {
	f.record("GetDNSRecordSet", sld, tld)
	if f.err != nil {
		return nil, f.err
	}
	return f.set, nil
}

func (f *fakeBackend) SetDNSRecordSet(_ context.Context, sld, tld string, set DNSRecordSet) error {
	f.record("SetDNSRecordSet", sld, tld, set)
	return f.err
}

func (f *fakeBackend) SetCustomNameservers(_ context.Context, domain string, nameservers []string) (*DomainNameserverUpdate, error) {
	f.record("SetCustomNameservers", domain, nameservers)
	if f.err != nil {
		return nil, f.err
	}
	return &DomainNameserverUpdate{Domain: domain, Updated: true, NameServers: nameservers}, nil
}

func (f *fakeBackend) wrote() bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c.method, "Set") {
			return true
		}
	}
	return false
}

func sampleSet() *DNSRecordSet {
	return &DNSRecordSet{EmailType: "MX", Records: []DNSRecord{
		{ID: 1, Type: "A", Host: "@", Value: "203.0.113.10", TTL: 300},
		{ID: 2, Type: "MX", Host: "@", Value: "mail.example.com", TTL: 300, MXPref: 10},
		{ID: 3, Type: "TXT", Host: "@", Value: "v=spf1 -all", TTL: 1800},
	}}
}

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{
		SecretAPIUser: "api-user-value", SecretAPIKey: "api-key-value-1234", SecretUsername: "account-name",
	}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func callTool(p *Plugin, operation string, args map[string]any) (subprocess.MCPCallResult, error) {
	return p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
}

func mustCall(t *testing.T, p *Plugin, operation string, args map[string]any) subprocess.MCPCallResult {
	t.Helper()
	result, err := callTool(p, operation, args)
	if err != nil || result.IsError {
		t.Fatalf("%s: err = %v, content = %s", operation, err, result.Content)
	}
	return result
}

// failure returns a failed call's code and message, whether the plugin coded
// it (a tool error result) or not (an error).
func failure(t *testing.T, result subprocess.MCPCallResult, err error) (cerbplugin.ErrorCode, string) {
	t.Helper()
	if err != nil {
		return "", err.Error()
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !result.IsError || !ok {
		t.Fatalf("want a failure, got %s", result.Content)
	}
	return code, message
}

func validArgs(operation string) map[string]any {
	switch operation {
	case "set_dns_record_set":
		return map[string]any{
			"domain": "example.com", "email_type": "MX", argAcknowledged: true,
			"records": []any{
				map[string]any{"type": "A", "host": "@", "value": "203.0.113.10", "ttl": float64(300)},
				map[string]any{"type": "MX", "host": "@", "value": "mail.example.com", "ttl": float64(300), "mx_pref": float64(10)},
			},
		}
	case "set_custom_nameservers":
		return map[string]any{"domain": "example.com", "nameservers": []any{"ns1.example.net", "ns2.example.net"}, argAcknowledged: true}
	case "list_domains":
		return map[string]any{}
	default:
		return map[string]any{"domain": "example.com"}
	}
}

func TestEveryDeclaredOperationIsServed(t *testing.T) {
	backend := &fakeBackend{set: sampleSet(), status: &DomainStatus{Domain: "example.com"}}
	p := loadedPlugin(t, backend)
	for _, op := range Definition().Operations {
		mustCall(t, p, op.Name, validArgs(op.Name))
	}
}

// The operations the built-in declared, less the two per-record writes it
// only ever refused.
func TestOperationSetMatchesTheBuiltIn(t *testing.T) {
	var got []string
	for _, op := range Definition().Operations {
		got = append(got, op.Name)
	}
	sort.Strings(got)
	want := []string{"get_dns_record_set", "get_domain_status", "list_dns_records", "list_domains", "set_custom_nameservers", "set_dns_record_set"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

// A write is ack-gated and previews; a read is neither.
func TestWriteOperationsAreExactlyTheAckGatedOnes(t *testing.T) {
	for _, op := range Definition().Operations {
		write := writeOperations[op.Name]
		if op.RequiresAck != write || op.SupportsDry != write || op.Effect.ReadOnly() == write {
			t.Errorf("%s: effect=%s requires_ack=%v supports_dry=%v, but writeOperations says write=%v",
				op.Name, op.Effect, op.RequiresAck, op.SupportsDry, write)
		}
	}
}

// The credentials carry over only if the id and every secret name are the
// built-in's, and client_ip reaches the plugin only because it is declared.
func TestIdentityMatchesTheBuiltInSoCredentialsCarryOver(t *testing.T) {
	def := Definition()
	if def.ID != "namecheap" {
		t.Fatalf("id = %q", def.ID)
	}
	want := map[string]bool{"api_user": true, "api_key": true, "username": true, "client_ip": false}
	if len(def.Config.Secrets) != len(want) {
		t.Fatalf("secrets = %+v", def.Config.Secrets)
	}
	for _, s := range def.Config.Secrets {
		required, ok := want[s.Name]
		if !ok || s.Required != required || s.Env != "CERBERUS_NAMECHEAP_"+strings.ToUpper(s.Name) {
			t.Errorf("secret %+v does not match the built-in's", s)
		}
	}
}

func TestClientIPDefaultsAndComesFromTheHost(t *testing.T) {
	var got Credentials
	p := &Plugin{newBackend: func(c Credentials) Backend { got = c; return &fakeBackend{} }}
	config := map[string]string{SecretAPIUser: "u", SecretAPIKey: "k", SecretUsername: "n"}
	_, _ = p.Init(context.Background(), subprocess.InitParams{Config: config})
	_, _ = p.Load(context.Background())
	if got.ClientIP != DefaultClientIP {
		t.Fatalf("ClientIP = %q, want the default", got.ClientIP)
	}
	config[SecretClientIP] = "198.51.100.7"
	p = &Plugin{newBackend: func(c Credentials) Backend { got = c; return &fakeBackend{} }}
	_, _ = p.Init(context.Background(), subprocess.InitParams{Config: config})
	_, _ = p.Load(context.Background())
	if got.ClientIP != "198.51.100.7" || got.APIKey != "k" {
		t.Fatalf("credentials = %+v, want the host's values", got)
	}
}

// The per-record writes are refused by name, before any credential is used,
// with the command that replaced them.
func TestPerRecordWritesAreRefused(t *testing.T) {
	p := &Plugin{} // no credentials, no backend
	for _, tool := range refusedTools {
		result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: tool, Arguments: map[string]any{argAcknowledged: true}})
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%s: code = %q, want invalid_args", tool, code)
		}
		if !strings.Contains(message, "cerberus connectors exec namecheap set_dns_record_set") {
			t.Errorf("%s: refusal does not name the replacement: %s", tool, message)
		}
	}
}

func TestSetDNSRecordSetWritesTheValidatedSet(t *testing.T) {
	backend := &fakeBackend{set: sampleSet()}
	result := mustCall(t, loadedPlugin(t, backend), "set_dns_record_set", validArgs("set_dns_record_set"))
	var got DNSRecordSet
	if err := json.Unmarshal(result.Content, &got); err != nil || got.EmailType != "MX" || len(got.Records) != 2 {
		t.Fatalf("result = %s (%v)", result.Content, err)
	}
	last := backend.calls[len(backend.calls)-1]
	if last.method != "SetDNSRecordSet" || last.args[0] != "example" || last.args[1] != "com" {
		t.Fatalf("calls = %+v", backend.calls)
	}
	if got.Records[1].MXPref != 10 || got.Records[0].TTL != 300 {
		t.Fatalf("typed fields lost: %+v", got.Records)
	}
}

// The record-set rules hold on both paths, and a refusal never reaches
// Namecheap: not the write, and not the preview's read.
func TestSetDNSRecordSetValidation(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown email_type": func(a map[string]any) { a["email_type"] = "SMTP" },
		"no email_type":      func(a map[string]any) { delete(a, "email_type") },
		"no records":         func(a map[string]any) { delete(a, "records") },
		"records not a list": func(a map[string]any) { a["records"] = "A @ 1.2.3.4" },
		"record without value": func(a map[string]any) {
			a["records"] = []any{map[string]any{"type": "A", "host": "@"}}
		},
		"MX under FWD": func(a map[string]any) { a["email_type"] = "FWD" },
		"bad domain":   func(a map[string]any) { a["domain"] = "localhost" },
		"bad ttl": func(a map[string]any) {
			a["records"] = []any{map[string]any{"type": "A", "host": "@", "value": "203.0.113.10", "ttl": "soon"}}
		},
	}
	for name, mutate := range cases {
		for _, dryRun := range []bool{false, true} {
			args := validArgs("set_dns_record_set")
			mutate(args)
			if dryRun {
				args[argDryRun] = true
			}
			backend := &fakeBackend{set: sampleSet()}
			result, err := callTool(loadedPlugin(t, backend), "set_dns_record_set", args)
			code, message := failure(t, result, err)
			if code != cerbplugin.ErrorInvalidArgs {
				t.Errorf("%s (dry_run=%v): code = %q, want invalid_args: %s", name, dryRun, code, message)
			}
			if len(backend.calls) != 0 {
				t.Errorf("%s (dry_run=%v): reached Namecheap: %+v", name, dryRun, backend.calls)
			}
		}
	}
}

// Decision 9: the preview reads the zone as it is and says what would change,
// and never writes.
func TestSetDNSRecordSetPreviewIsADiffAgainstTheCurrentRecords(t *testing.T) {
	backend := &fakeBackend{set: sampleSet()}
	args := validArgs("set_dns_record_set")
	args["email_type"] = "NONE"
	args["records"] = []any{
		map[string]any{"type": "A", "host": "@", "value": "203.0.113.10", "ttl": float64(300)},      // unchanged
		map[string]any{"type": "CNAME", "host": "www", "value": "example.com", "ttl": float64(300)}, // added
	}
	args[argDryRun] = true
	result := mustCall(t, loadedPlugin(t, backend), "set_dns_record_set", args)
	if backend.wrote() {
		t.Fatalf("a dry run wrote: %+v", backend.calls)
	}
	if len(backend.calls) != 1 || backend.calls[0].method != "GetDNSRecordSet" {
		t.Fatalf("the preview should make exactly one read: %+v", backend.calls)
	}
	var preview DryRunPreview
	if err := json.Unmarshal(result.Content, &preview); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !preview.DryRun || preview.Summary != "Would replace every Namecheap DNS host record and explicitly set email routing." ||
		preview.Target["domain"] != "example.com" || len(preview.Warnings) != 1 || preview.Warnings[0] != setHostsWarning {
		t.Fatalf("preview = %+v", preview)
	}
	d := preview.Diff
	if d == nil || len(d.Add) != 1 || d.Add[0].Type != "CNAME" || len(d.Unchanged) != 1 || d.Unchanged[0].ID != 1 || len(d.Remove) != 2 {
		t.Fatalf("diff = %+v", d)
	}
	if d.EmailType == nil || d.EmailType.From != "MX" || d.EmailType.To != "NONE" {
		t.Fatalf("email change = %+v", d.EmailType)
	}
}

// A preview that cannot read the zone fails, coded, rather than claiming an
// empty diff.
func TestSetDNSRecordSetPreviewFailsWhenTheReadFails(t *testing.T) {
	backend := &fakeBackend{err: &APIError{Action: "namecheap list dns", Numbers: []string{"1011102"}, Message: "API Key is invalid or API access has not been enabled"}}
	args := validArgs("set_dns_record_set")
	args[argDryRun] = true
	result, err := callTool(loadedPlugin(t, backend), "set_dns_record_set", args)
	code, message := failure(t, result, err)
	if code != cerbplugin.ErrorCredentialMissing || !strings.Contains(message, "API Key is invalid") {
		t.Fatalf("code = %q, message = %s", code, message)
	}
	// And without credentials at all, the preview cannot run.
	result, err = callTool(&Plugin{}, "set_dns_record_set", args)
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorCredentialMissing {
		t.Fatalf("preview without credentials: code = %q", code)
	}
}

func TestSetCustomNameserversPreviewAndValidation(t *testing.T) {
	backend := &fakeBackend{}
	args := validArgs("set_custom_nameservers")
	args[argDryRun] = true
	result := mustCall(t, loadedPlugin(t, backend), "set_custom_nameservers", args)
	var preview DryRunPreview
	_ = json.Unmarshal(result.Content, &preview)
	if len(backend.calls) != 0 || preview.Summary != "Would switch a Namecheap domain to custom nameservers." || preview.Warnings[0] != nameserverWarning {
		t.Fatalf("preview = %+v, calls = %+v", preview, backend.calls)
	}
	for _, dryRun := range []bool{false, true} {
		args := validArgs("set_custom_nameservers")
		args["nameservers"] = []any{"ns1.example.net", " "}
		args[argDryRun] = dryRun
		result, err := callTool(loadedPlugin(t, backend), "set_custom_nameservers", args)
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("one nameserver (dry_run=%v): code = %q", dryRun, code)
		}
	}
}

func TestUnacknowledgedWriteIsRefused(t *testing.T) {
	backend := &fakeBackend{set: sampleSet()}
	for op := range writeOperations {
		args := validArgs(op)
		delete(args, argAcknowledged)
		if _, err := callTool(loadedPlugin(t, backend), op, args); err == nil || !strings.Contains(err.Error(), "requires acknowledgment") {
			t.Errorf("%s: err = %v", op, err)
		}
	}
	if backend.wrote() {
		t.Fatal("an unacknowledged write reached Namecheap")
	}
}

func TestDryRunOnAReadIsRefused(t *testing.T) {
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "list_domains", map[string]any{argDryRun: true})
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
		t.Fatalf("code = %q", code)
	}
}

func TestMissingCredentialLoadsAndExplains(t *testing.T) {
	for _, name := range []string{SecretAPIUser, SecretAPIKey, SecretUsername} {
		t.Setenv(envVar(name), "")
	}
	p := &Plugin{newBackend: func(Credentials) Backend { t.Fatal("backend built without credentials"); return nil }}
	_, _ = p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretAPIKey: "only-the-key-1234"}})
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	status, _ := p.Health(context.Background())
	if status.OK || !strings.Contains(status.Message, envVar(SecretAPIUser)) {
		t.Fatalf("Health = %+v", status)
	}
	for _, op := range Definition().Operations {
		result, err := callTool(p, op.Name, validArgs(op.Name))
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorCredentialMissing || message != errMissingCredential.Error() {
			t.Errorf("%s: code = %q, message = %q", op.Name, code, message)
		}
	}
	// A preview that needs no read keeps working.
	args := validArgs("set_custom_nameservers")
	args[argDryRun] = true
	if result, err := callTool(p, "set_custom_nameservers", args); err != nil || result.IsError {
		t.Fatalf("nameserver dry run without credentials: %v %s", err, result.Content)
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	_, err := loadedPlugin(t, &fakeBackend{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_namecheap_nope"})
	if err == nil {
		t.Fatal("want an error")
	}
}

func TestBackendErrorsPropagateUncoded(t *testing.T) {
	boom := errors.New("unexpected status 502: bad gateway")
	_, err := callTool(loadedPlugin(t, &fakeBackend{err: boom}), "list_domains", nil)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestIsValid(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := PluginYAML().Validate(t.TempDir()); err != nil {
		t.Fatalf("generated plugin.yaml: %v", err)
	}
}

// The sandbox is selected by the config field, or by the environment for a
// binary run directly; production is the default.
func TestSandboxSelectsTheSandboxAPI(t *testing.T) {
	t.Setenv(SandboxEnvVar, "")
	creds := map[string]string{SecretAPIUser: "u", SecretAPIKey: "k", SecretUsername: "n"}
	build := func(config map[string]string) *Client {
		p := New()
		_, _ = p.Init(context.Background(), subprocess.InitParams{Config: config})
		_, _ = p.Load(context.Background())
		client, ok := p.backend.(*Client)
		if !ok {
			t.Fatalf("backend = %T", p.backend)
		}
		return client
	}
	if got := build(creds).baseURL; got != APIBaseURL {
		t.Fatalf("default base URL = %q, want production", got)
	}
	withSandbox := map[string]string{ConfigSandbox: "true"}
	for k, v := range creds {
		withSandbox[k] = v
	}
	if got := build(withSandbox).baseURL; got != SandboxBaseURL {
		t.Fatalf("sandbox config: base URL = %q", got)
	}
	t.Setenv(SandboxEnvVar, "1")
	if got := build(creds).baseURL; got != SandboxBaseURL {
		t.Fatalf("%s=1: base URL = %q", SandboxEnvVar, got)
	}
}
