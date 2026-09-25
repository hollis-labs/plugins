package cloudflareplugin

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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
	zones   []Zone
	records []DNSRecord
	err     error
	calls   []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) ListZones(context.Context) ([]Zone, error) {
	f.record("ListZones")
	return f.zones, f.err
}

func (f *fakeBackend) CreateZone(_ context.Context, accountID, name, zoneType string) (*Zone, error) {
	f.record("CreateZone", accountID, name, zoneType)
	if f.err != nil {
		return nil, f.err
	}
	return &Zone{ID: "zone-new", Name: name, Status: "pending"}, nil
}

func (f *fakeBackend) ListDNSRecords(_ context.Context, zoneID string) ([]DNSRecord, error) {
	f.record("ListDNSRecords", zoneID)
	return f.records, f.err
}

func (f *fakeBackend) CreateDNSRecord(_ context.Context, zoneID string, rec DNSRecord) (*DNSRecord, error) {
	f.record("CreateDNSRecord", zoneID, rec)
	if f.err != nil {
		return nil, f.err
	}
	rec.ID = "record-new"
	return &rec, nil
}

func (f *fakeBackend) DeleteDNSRecord(_ context.Context, zoneID, recordID string) error {
	f.record("DeleteDNSRecord", zoneID, recordID)
	return f.err
}

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
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
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return result
}

func decode[T any](t *testing.T, result subprocess.MCPCallResult) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(result.Content, &out); err != nil {
		t.Fatalf("decode %s: %v", result.Content, err)
	}
	return out
}

// validArgs is a complete, acknowledged argument set for every operation, so a
// test can iterate the manifest without knowing each schema.
func validArgs(operation string) map[string]any {
	switch operation {
	case "create_zone":
		return map[string]any{"account_id": "acct", "name": "example.com", argAcknowledged: true}
	case "list_dns_records":
		return map[string]any{"zone_id": "zone"}
	case "create_dns_record":
		return map[string]any{"zone_id": "zone", "type": "A", "name": "app", "content": "203.0.113.10", argAcknowledged: true}
	case "delete_dns_record":
		return map[string]any{"zone_id": "zone", "record_id": "rec", argAcknowledged: true}
	default:
		return map[string]any{}
	}
}

// Every operation the manifest declares must be routable by the tool name the
// host derives from it. This is the contract between the two halves.
func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	for _, op := range Definition().Operations {
		if _, err := callTool(p, op.Name, validArgs(op.Name)); err != nil {
			t.Fatalf("operation %q is declared but not served: %v", op.Name, err)
		}
	}
}

// The operation set must match the compiled-in connector's, so a caller sees
// the same contract either side of the move. ListTunnels was implemented there
// but never declared; it stays undeclared here.
func TestOperationSetMatchesTheBuiltIn(t *testing.T) {
	var got []string
	for _, op := range Definition().Operations {
		got = append(got, op.Name)
	}
	want := []string{"list_zones", "create_zone", "list_dns_records", "create_dns_record", "delete_dns_record"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestWriteOperationsAreExactlyTheDestructiveOnes(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.Destructive != writeOperations[op.Name] {
			t.Errorf("%s: Destructive=%v but writeOperations says %v", op.Name, op.Destructive, writeOperations[op.Name])
		}
		if op.SupportsDry != writeOperations[op.Name] {
			t.Errorf("%s: SupportsDry=%v; every write previews and no read does", op.Name, op.SupportsDry)
		}
	}
}

func TestListZonesReturnsDTOs(t *testing.T) {
	backend := &fakeBackend{zones: []Zone{{ID: "z1", Name: "example.com", Status: "active", NameServers: []string{"a.ns"}}}}
	zones := decode[[]Zone](t, mustCall(t, loadedPlugin(t, backend), "list_zones", nil))
	if len(zones) != 1 || zones[0].ID != "z1" || zones[0].NameServers[0] != "a.ns" {
		t.Fatalf("zones = %+v", zones)
	}
}

func TestListDNSRecordsPassesTheZone(t *testing.T) {
	backend := &fakeBackend{records: []DNSRecord{{ID: "r1", Type: "TXT", Name: "example.com", Content: "v=spf1 -all"}}}
	records := decode[[]DNSRecord](t, mustCall(t, loadedPlugin(t, backend), "list_dns_records", map[string]any{"zone_id": "z1"}))
	if len(records) != 1 || records[0].ID != "r1" {
		t.Fatalf("records = %+v", records)
	}
	if backend.calls[0].args[0] != "z1" {
		t.Fatalf("zone passed = %v", backend.calls[0].args)
	}
}

func TestCreateZoneDefaultsToFull(t *testing.T) {
	backend := &fakeBackend{}
	mustCall(t, loadedPlugin(t, backend), "create_zone", map[string]any{"account_id": "acct", "name": "example.com", argAcknowledged: true})
	if got := backend.calls[0].args; !reflect.DeepEqual(got, []any{"acct", "example.com", ZoneTypeFull}) {
		t.Fatalf("CreateZone args = %v", got)
	}
}

// Arguments arrive as JSON from MCP and the API, and as strings from
// `--arg key=value` on the CLI. Both must produce the same record.
func TestCreateDNSRecordCoercesCLIStrings(t *testing.T) {
	priority := 10
	want := DNSRecord{Type: "MX", Name: "@", Content: "mail.example.com", TTL: 300, Proxied: true, Priority: &priority}

	for name, args := range map[string]map[string]any{
		"json": {"zone_id": "z", "type": "MX", "name": "@", "content": "mail.example.com", "ttl": float64(300), "proxied": true, "priority": float64(10), argAcknowledged: true},
		"cli":  {"zone_id": "z", "type": "MX", "name": "@", "content": "mail.example.com", "ttl": "300", "proxied": "true", "priority": "10", argAcknowledged: "true"},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &fakeBackend{}
			mustCall(t, loadedPlugin(t, backend), "create_dns_record", args)
			got := backend.calls[0].args[1].(DNSRecord)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("record = %+v, want %+v", got, want)
			}
		})
	}
}

func TestCreateDNSRecordDefaultsTTLToAutomaticAndOmitsPriority(t *testing.T) {
	backend := &fakeBackend{}
	mustCall(t, loadedPlugin(t, backend), "create_dns_record", validArgs("create_dns_record"))
	got := backend.calls[0].args[1].(DNSRecord)
	if got.TTL != 1 || got.Priority != nil || got.Proxied {
		t.Fatalf("record = %+v, want ttl 1, no priority, not proxied", got)
	}
}

func TestCreateDNSRecordRejectsBadNumbers(t *testing.T) {
	backend := &fakeBackend{}
	args := validArgs("create_dns_record")
	args["ttl"] = "soon"
	args["priority"] = float64(-1)
	_, err := callTool(loadedPlugin(t, backend), "create_dns_record", args)
	if err == nil || !strings.Contains(err.Error(), "ttl must be a whole number") || !strings.Contains(err.Error(), "priority must not be negative") {
		t.Fatalf("err = %v", err)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend called despite invalid arguments: %+v", backend.calls)
	}
}

func TestDeleteDNSRecordReportsWhatWasDeleted(t *testing.T) {
	backend := &fakeBackend{}
	got := decode[DeletedRecord](t, mustCall(t, loadedPlugin(t, backend), "delete_dns_record", validArgs("delete_dns_record")))
	if got != (DeletedRecord{Deleted: true, ZoneID: "zone", RecordID: "rec"}) {
		t.Fatalf("result = %+v", got)
	}
}

func TestMissingArgumentsAreAllReported(t *testing.T) {
	backend := &fakeBackend{}
	_, err := callTool(loadedPlugin(t, backend), "create_dns_record", map[string]any{argAcknowledged: true})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, key := range []string{"zone_id", "type", "name", "content"} {
		if !strings.Contains(err.Error(), key+" is required") {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend called: %+v", backend.calls)
	}
}

// The host refuses an unacknowledged destructive call before it gets here.
// The plugin refuses it too, so its own contract does not depend on the caller.
func TestUnacknowledgedWriteIsRefused(t *testing.T) {
	for op := range writeOperations {
		t.Run(op, func(t *testing.T) {
			backend := &fakeBackend{}
			args := validArgs(op)
			delete(args, argAcknowledged)
			_, err := callTool(loadedPlugin(t, backend), op, args)
			if err == nil || !strings.Contains(err.Error(), "requires acknowledgment") {
				t.Fatalf("err = %v, want an acknowledgment refusal", err)
			}
			if len(backend.calls) != 0 {
				t.Fatalf("backend called without acknowledgment: %+v", backend.calls)
			}
		})
	}
}

// A dry run never reaches Cloudflare, needs no acknowledgment and no
// credential, and returns the host's preview shape with the built-in's
// summaries, targets and inputs.
func TestDryRunPreviewsWithoutCallingCloudflare(t *testing.T) {
	cases := []struct {
		op   string
		args map[string]any
		want DryRunPreview
	}{
		{
			op:   "create_zone",
			args: map[string]any{"account_id": "acct", "name": "example.com"},
			want: DryRunPreview{DryRun: true, Connector: "cloudflare", Operation: "create_zone", Summary: "Would create a Cloudflare zone.",
				Target: map[string]any{"account_id": "acct", "name": "example.com"},
				Input:  map[string]any{"type": "full"}},
		},
		{
			op:   "create_dns_record",
			args: map[string]any{"zone_id": "z", "type": "A", "name": "app", "content": "203.0.113.10", "ttl": "300"},
			want: DryRunPreview{DryRun: true, Connector: "cloudflare", Operation: "create_dns_record", Summary: "Would create a Cloudflare DNS record.",
				Target: map[string]any{"zone_id": "z", "name": "app", "type": "A"},
				Input:  map[string]any{"content": "203.0.113.10", "ttl": float64(300), "proxied": false, "priority": nil}},
		},
		{
			op:   "delete_dns_record",
			args: map[string]any{"zone_id": "z", "record_id": "r"},
			want: DryRunPreview{DryRun: true, Connector: "cloudflare", Operation: "delete_dns_record", Summary: "Would delete a Cloudflare DNS record.",
				Target: map[string]any{"zone_id": "z", "record_id": "r"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			// No backend and no token: a preview must work without either.
			p := &Plugin{newBackend: func(string) Backend { t.Fatal("backend built for a dry run"); return nil }}
			if _, err := p.Load(context.Background()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			tc.args[argDryRun] = true
			got := decode[DryRunPreview](t, mustCall(t, p, tc.op, tc.args))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("preview =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

func TestDryRunOnAReadIsRefused(t *testing.T) {
	backend := &fakeBackend{}
	_, err := callTool(loadedPlugin(t, backend), "list_zones", map[string]any{argDryRun: true})
	if err == nil || !strings.Contains(err.Error(), "no dry-run preview") {
		t.Fatalf("err = %v", err)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend called: %+v", backend.calls)
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	_, err := loadedPlugin(t, &fakeBackend{}).MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_cloudflare_list_tunnels"})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("err = %v", err)
	}
}

func TestBackendErrorsPropagate(t *testing.T) {
	_, err := callTool(loadedPlugin(t, &fakeBackend{err: errors.New("zone not found")}), "list_dns_records", map[string]any{"zone_id": "z"})
	if err == nil || !strings.Contains(err.Error(), "zone not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitAdvertisesTheManifestIdentity(t *testing.T) {
	result, err := New().Init(context.Background(), subprocess.InitParams{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.ID != ConnectorID || result.Version != Version || result.Protocol != subprocess.ProtocolVersion {
		t.Fatalf("Init = %+v", result)
	}
}

func TestGeneratedPluginYAMLIsValid(t *testing.T) {
	spec := PluginYAML()
	if err := spec.Validate(t.TempDir()); err != nil {
		t.Fatalf("generated plugin.yaml invalid: %v", err)
	}
	if spec.ID != ConnectorID {
		t.Fatalf("plugin id = %q, want %q", spec.ID, ConnectorID)
	}
	if !strings.HasSuffix(spec.Entrypoint.Command, BinaryName) {
		t.Fatalf("entrypoint = %q, want it to end in %q", spec.Entrypoint.Command, BinaryName)
	}
	if len(spec.Cerberus.Connector.Operations) != len(Definition().Operations) {
		t.Fatal("manifest operations drifted from the connector definition")
	}
}

// The id and secret name are what existing credential references were written
// against. Changing either would silently orphan them.
func TestIdentityMatchesTheBuiltInSoCredentialsCarryOver(t *testing.T) {
	if ConnectorID != "cloudflare" {
		t.Fatalf("ConnectorID = %q; the host resolves secrets as <id>/<name>, so this must stay cloudflare", ConnectorID)
	}
	secrets := Definition().Config.Secrets
	if len(secrets) != 1 || secrets[0].Name != "api_token" || secrets[0].Env != "CERBERUS_CLOUDFLARE_API_TOKEN" || !secrets[0].Required {
		t.Fatalf("secrets = %+v, want exactly a required api_token read as CERBERUS_CLOUDFLARE_API_TOKEN", secrets)
	}
	if secrets[0].Name != SecretAPIToken {
		t.Fatalf("declared secret %q does not match the key the plugin reads, %q", secrets[0].Name, SecretAPIToken)
	}
}

func TestTokenComesFromHostInitConfig(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	var got string
	p := &Plugin{newBackend: func(token string) Backend { got = token; return &fakeBackend{} }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretAPIToken: "host-resolved-token"}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "host-resolved-token" {
		t.Fatalf("token = %q, want the value the host resolved", got)
	}
}

func TestDirectRunFallsBackToTheEnvironment(t *testing.T) {
	t.Setenv(TokenEnvVar, "direct-run-token")
	var got string
	p := &Plugin{newBackend: func(token string) Backend { got = token; return &fakeBackend{} }}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "direct-run-token" {
		t.Fatalf("token = %q, want the direct-run fallback", got)
	}
}

// A missing credential must not fail the load. Every Cloudflare call then
// fails with the guidance, Health reports it, and dry runs keep working.
func TestMissingCredentialLoadsAndExplains(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	p := &Plugin{newBackend: func(string) Backend { t.Fatal("backend built without a token"); return nil }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load without a token: %v", err)
	}
	status, err := p.Health(context.Background())
	if err != nil || status.OK || !strings.Contains(status.Message, TokenEnvVar) {
		t.Fatalf("Health = %+v, %v; want not OK and naming %s", status, err, TokenEnvVar)
	}
	for _, op := range Definition().Operations {
		_, err := callTool(p, op.Name, validArgs(op.Name))
		if !errors.Is(err, errMissingCredential) && (err == nil || err.Error() != errMissingCredential.Error()) {
			t.Errorf("%s: err = %v, want the missing-credential guidance", op.Name, err)
		}
	}
	if _, err := callTool(p, "delete_dns_record", map[string]any{"zone_id": "z", "record_id": "r", argDryRun: true}); err != nil {
		t.Fatalf("dry run without a credential: %v", err)
	}
}

func TestHealthReportsAConfiguredToken(t *testing.T) {
	status, err := loadedPlugin(t, &fakeBackend{}).Health(context.Background())
	if err != nil || !status.OK {
		t.Fatalf("Health = %+v, %v", status, err)
	}
}
