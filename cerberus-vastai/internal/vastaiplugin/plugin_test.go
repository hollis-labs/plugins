package vastaiplugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	offers    []Offer
	instances []Instance
	charges   ChargeSet
	account   Account
	err       error
	// getErr fails only GetInstance, to model a create whose read-back fails.
	getErr     error
	accountErr error
	createdID  int
	calls      []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) methods() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.method)
	}
	return out
}

func (f *fakeBackend) GetAccount(context.Context) (*Account, error) {
	f.record("GetAccount")
	if f.err != nil {
		return nil, f.err
	}
	if f.accountErr != nil {
		return nil, f.accountErr
	}
	return &f.account, nil
}

func (f *fakeBackend) SearchOffers(_ context.Context, q OfferQuery) ([]Offer, error) {
	f.record("SearchOffers", q)
	return f.offers, f.err
}

func (f *fakeBackend) GetOffer(_ context.Context, id int) (*Offer, error) {
	f.record("GetOffer", id)
	if f.err != nil {
		return nil, f.err
	}
	for _, o := range f.offers {
		if o.ID == id {
			return &o, nil
		}
	}
	return nil, nil
}

func (f *fakeBackend) ListInstances(context.Context) (InstanceList, error) {
	f.record("ListInstances")
	return InstanceList{Instances: f.instances, Count: len(f.instances)}, f.err
}

func (f *fakeBackend) GetInstance(_ context.Context, id int) (*Instance, error) {
	f.record("GetInstance", id)
	if f.err != nil {
		return nil, f.err
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, i := range f.instances {
		if i.ID == id {
			return &i, nil
		}
	}
	return &Instance{ID: id, Label: "bench", State: "running", GPUName: "RTX 4090", NumGPUs: 1, DPHTotal: 0.5, StorageDPH: 0.01, RuntimeHours: 2, EstimatedCost: 1}, nil
}

func (f *fakeBackend) CreateInstance(_ context.Context, req CreateRequest) (int, error) {
	f.record("CreateInstance", req)
	if f.err != nil {
		return 0, f.err
	}
	if f.createdID == 0 {
		f.createdID = 17
	}
	return f.createdID, nil
}

func (f *fakeBackend) SetState(_ context.Context, id int, state string) error {
	f.record("SetState", id, state)
	return f.err
}

func (f *fakeBackend) SetLabel(_ context.Context, id int, label string) error {
	f.record("SetLabel", id, label)
	return f.err
}

func (f *fakeBackend) Destroy(_ context.Context, id int) error {
	f.record("Destroy", id)
	return f.err
}

func (f *fakeBackend) Charges(_ context.Context, from, to time.Time) (ChargeSet, error) {
	f.record("Charges", from, to)
	return f.charges, f.err
}

func (f *fakeBackend) Logs(_ context.Context, id int, req LogsRequest) (LogsResult, error) {
	f.record("Logs", id, req)
	return LogsResult{InstanceID: id, Logs: "line one\nline two\n"}, f.err
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
	if result.IsError {
		t.Fatalf("%s failed: %s", operation, result.Content)
	}
	return result
}

// failure reads a failed call either way it can come back: coded, as an error
// result the host maps onto its own code, or as a plain error.
func failure(t *testing.T, result subprocess.MCPCallResult, err error) (cerbplugin.ErrorCode, string) {
	t.Helper()
	if err != nil {
		return "", err.Error()
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !result.IsError || !ok {
		t.Fatalf("call succeeded (%s), want a failure", result.Content)
	}
	return code, message
}

func decode[T any](t *testing.T, result subprocess.MCPCallResult) T {
	t.Helper()
	content, _ := cerbplugin.SplitTelemetry(result.Content)
	var out T
	if err := json.Unmarshal(content, &out); err != nil {
		t.Fatalf("decode %s: %v", content, err)
	}
	return out
}

// validArgs is a complete, acknowledged argument set for every operation, so a
// test can iterate the manifest without knowing each schema.
func validArgs(operation string) map[string]any {
	switch operation {
	case "get_account", "search_offers", "list_instances", "cost_report":
		return map[string]any{}
	case "create_instance":
		return map[string]any{"offer_id": float64(7), "image": "vllm/vllm-openai:latest", "label": "bench-48gb", argAcknowledged: true}
	case "set_label":
		return map[string]any{"instance_id": float64(42), "label": "bench-80gb", argAcknowledged: true}
	case "start", "stop", "destroy":
		return map[string]any{"instance_id": float64(42), argAcknowledged: true}
	default:
		return map[string]any{"instance_id": float64(42)}
	}
}

func TestEveryDeclaredOperationIsServed(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{offers: []Offer{{ID: 7, DPHTotal: 0.4}}})
	for _, op := range Definition().Operations {
		if _, err := callTool(p, op.Name, validArgs(op.Name)); err != nil {
			t.Fatalf("operation %q is declared but not served: %v", op.Name, err)
		}
	}
}

// The contract is the source of truth: the plugin gates on acknowledgment
// exactly the operations the contract says need it, and previews exactly the
// ones it says preview.
func TestGatesMatchTheContract(t *testing.T) {
	wantEffect := map[string]string{
		"get_account": "read", "search_offers": "read", "list_instances": "read", "get_instance": "read",
		"status": "read", "cost_report": "read", "get_logs": "read_sensitive",
		"create_instance": "write", "set_label": "write", "start": "lifecycle", "stop": "lifecycle", "destroy": "destructive",
	}
	if len(wantEffect) != len(Definition().Operations) {
		t.Fatalf("test knows %d operations, Definition has %d", len(wantEffect), len(Definition().Operations))
	}
	for _, op := range Definition().Operations {
		if string(op.Effect) != wantEffect[op.Name] {
			t.Errorf("%s: effect = %s, want %s", op.Name, op.Effect, wantEffect[op.Name])
		}
		if op.RequiresAck != ackOperations[op.Name] || op.Effect.ReadOnly() == ackOperations[op.Name] {
			t.Errorf("%s: effect=%s requires_ack=%v, but ackOperations says %v", op.Name, op.Effect, op.RequiresAck, ackOperations[op.Name])
		}
		if op.SupportsDry != previewOperations[op.Name] {
			t.Errorf("%s: supports_dry=%v, but previewOperations says %v", op.Name, op.SupportsDry, previewOperations[op.Name])
		}
		if op.Destructive != (op.Name == "destroy") {
			t.Errorf("%s: Destructive=%v; only destroy is destructive", op.Name, op.Destructive)
		}
	}
	for op := range previewOperations {
		if !ackOperations[op] {
			t.Errorf("%s previews but is not acknowledgment-gated", op)
		}
	}
	for _, op := range Definition().Operations {
		billable := op.Name == "create_instance" || op.Name == "start"
		if (op.Cost == "billable") != billable {
			t.Errorf("%s cost = %s", op.Name, op.Cost)
		}
	}
}

func TestInstanceIDIsValidated(t *testing.T) {
	for _, raw := range []any{nil, "", "abc", float64(4.5), float64(-1), float64(0)} {
		backend := &fakeBackend{}
		args := map[string]any{}
		if raw != nil {
			args["instance_id"] = raw
		}
		result, err := callTool(loadedPlugin(t, backend), "get_instance", args)
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "instance_id") {
			t.Errorf("instance_id %#v: %s %q, want invalid_args naming instance_id", raw, code, message)
		}
		if len(backend.calls) != 0 {
			t.Errorf("instance_id %#v reached the backend", raw)
		}
	}
}

func TestSearchOffersTranslatesFilters(t *testing.T) {
	backend := &fakeBackend{offers: []Offer{{ID: 1, DPHTotal: 0.4}, {ID: 2, DPHTotal: 0.6}}}
	got := decode[OfferList](t, mustCall(t, loadedPlugin(t, backend), "search_offers", map[string]any{
		"gpu_name":        []any{"RTX_4090", "A100 SXM4"},
		"min_vram_gb":     "48",
		"max_price":       1.5,
		"min_reliability": 0.98,
		"geolocation":     "us",
		"order_by":        "dlperf_per_dphtotal",
	}))
	if got.Count != 2 {
		t.Fatalf("offers = %+v", got)
	}
	q := backend.calls[0].args[0].(OfferQuery)
	if q.GPUNames[0] != "RTX 4090" || q.GPUNames[1] != "A100 SXM4" {
		t.Errorf("gpu names = %v, want underscores read as spaces", q.GPUNames)
	}
	if q.MinVRAMGB != 48 || q.MaxDPH != 1.5 || q.MinReliability != 0.98 || q.Geolocation[0] != "US" {
		t.Errorf("query = %+v", q)
	}
	if q.OrderBy != "dlperf_per_dphtotal" || !q.Descending {
		t.Errorf("a bigger-is-better column should sort descending: %+v", q)
	}
	if q.Limit != defaultOfferLimit {
		t.Errorf("limit = %d", q.Limit)
	}
}

func TestSearchOffersRefusesBadInput(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"bad type":  {"type": "spot"},
		"bad order": {"order_by": "price; drop"},
		"big limit": {"limit": float64(500)},
		"negative":  {"max_price": float64(-1)},
	} {
		backend := &fakeBackend{}
		result, err := callTool(loadedPlugin(t, backend), "search_offers", args)
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%s: code = %q", name, code)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s reached the backend", name)
		}
	}
}

func TestCreateInstanceRentsAndReadsItBack(t *testing.T) {
	backend := &fakeBackend{createdID: 99, instances: []Instance{{ID: 99, Label: "bench-48gb", State: "starting"}}}
	args := validArgs("create_instance")
	args["disk_gb"] = float64(80)
	args["onstart"] = "nvidia-smi"
	args["env"] = map[string]any{"HF_TOKEN": "hf_secret", "-p 8000:8000": "1"}
	got := decode[Instance](t, mustCall(t, loadedPlugin(t, backend), "create_instance", args))
	if got.ID != 99 {
		t.Fatalf("result = %+v, want the read-back instance", got)
	}
	req := backend.calls[0].args[0].(CreateRequest)
	if req.OfferID != 7 || req.Label != "bench-48gb" || req.DiskGB != 80 || req.Env["HF_TOKEN"] != "hf_secret" || req.TargetState != "" {
		t.Fatalf("create request = %+v", req)
	}
}

// A rented instance is billable. If reading it back fails, the create is still
// reported as a success carrying the id, never as an error inviting a retry
// and a second rental.
func TestCreateInstanceReportsTheIDWhenTheReadBackFails(t *testing.T) {
	backend := &fakeBackend{createdID: 77, getErr: errors.New("read failed")}
	got := decode[CreatedReference](t, mustCall(t, loadedPlugin(t, backend), "create_instance", validArgs("create_instance")))
	if got.InstanceID != 77 || got.Label != "bench-48gb" {
		t.Fatalf("result = %+v, want only the id and label", got)
	}
}

// Cost is attributed by label, so a rental without one is refused.
func TestCreateInstanceRequiresALabel(t *testing.T) {
	backend := &fakeBackend{}
	args := validArgs("create_instance")
	delete(args, "label")
	result, err := callTool(loadedPlugin(t, backend), "create_instance", args)
	code, message := failure(t, result, err)
	if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "label is required") {
		t.Fatalf("%s %q", code, message)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("backend called: %v", backend.methods())
	}
}

func TestCreateInstanceStartFalseCreatesItStopped(t *testing.T) {
	backend := &fakeBackend{}
	args := validArgs("create_instance")
	args["start"] = false
	mustCall(t, loadedPlugin(t, backend), "create_instance", args)
	if req := backend.calls[0].args[0].(CreateRequest); req.TargetState != "stopped" {
		t.Fatalf("target state = %q", req.TargetState)
	}
}

func TestMaxPriceRefusesAnOfferThatCostsMore(t *testing.T) {
	backend := &fakeBackend{offers: []Offer{{ID: 7, DPHTotal: 1.25}}}
	args := validArgs("create_instance")
	args["max_price"] = 1.0
	result, err := callTool(loadedPlugin(t, backend), "create_instance", args)
	code, message := failure(t, result, err)
	if code != cerbplugin.ErrorInvalidArgs || !strings.Contains(message, "above max_price") {
		t.Fatalf("%s %q", code, message)
	}
	for _, m := range backend.methods() {
		if m == "CreateInstance" {
			t.Fatal("rented despite the price ceiling")
		}
	}
}

func TestMaxPriceAllowsAnOfferThatFits(t *testing.T) {
	backend := &fakeBackend{offers: []Offer{{ID: 7, DPHTotal: 0.8}}}
	args := validArgs("create_instance")
	args["max_price"] = 1.0
	mustCall(t, loadedPlugin(t, backend), "create_instance", args)
	if got := backend.methods(); got[0] != "GetOffer" || got[1] != "CreateInstance" {
		t.Fatalf("calls = %v, want the price checked before renting", got)
	}
}

// With a ceiling set, an offer that cannot be priced is not rented unchecked.
func TestMaxPriceFailsClosedWhenTheOfferIsGone(t *testing.T) {
	backend := &fakeBackend{}
	args := validArgs("create_instance")
	args["max_price"] = 1.0
	result, err := callTool(loadedPlugin(t, backend), "create_instance", args)
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
		t.Fatalf("code = %q, want invalid_args for a missing offer", code)
	}
	for _, m := range backend.methods() {
		if m == "CreateInstance" {
			t.Fatal("rented an offer that could not be priced")
		}
	}
}

func TestLifecycleOperationsReportWhatTheyDid(t *testing.T) {
	cases := map[string]struct{ method, state string }{
		"start": {"SetState", "running"}, "stop": {"SetState", "stopped"}, "destroy": {"Destroy", ""},
	}
	for op, want := range cases {
		backend := &fakeBackend{}
		got := decode[InstanceAction](t, mustCall(t, loadedPlugin(t, backend), op, validArgs(op)))
		if got.InstanceID != 42 || got.Action != op || got.Label != "bench" || got.EstimatedCost != 1 {
			t.Errorf("%s: result = %+v", op, got)
		}
		last := backend.calls[len(backend.calls)-1]
		if last.method != want.method || last.args[0] != 42 || (want.state != "" && last.args[1] != want.state) {
			t.Errorf("%s: last call = %+v", op, last)
		}
	}
}

// stop and start report the rate the instance bills at afterwards.
func TestStopReportsTheDiskOnlyBurn(t *testing.T) {
	got := decode[InstanceAction](t, mustCall(t, loadedPlugin(t, &fakeBackend{}), "stop", validArgs("stop")))
	if got.BurnPerHour != 0.01 {
		t.Fatalf("burn after stop = %v, want the disk's 0.01", got.BurnPerHour)
	}
	got = decode[InstanceAction](t, mustCall(t, loadedPlugin(t, &fakeBackend{}), "start", validArgs("start")))
	if got.BurnPerHour != 0.5 {
		t.Fatalf("burn after start = %v, want the full 0.5", got.BurnPerHour)
	}
}

// Destroying must not depend on being able to read the instance first.
func TestDestroyProceedsWhenTheSnapshotFails(t *testing.T) {
	backend := &fakeBackend{getErr: errors.New("read failed")}
	got := decode[InstanceAction](t, mustCall(t, loadedPlugin(t, backend), "destroy", validArgs("destroy")))
	if got.Action != "destroy" || got.EstimatedCost != 0 {
		t.Fatalf("result = %+v", got)
	}
	if last := backend.calls[len(backend.calls)-1]; last.method != "Destroy" {
		t.Fatalf("calls = %v", backend.methods())
	}
}

func TestStatusNormalizesTheInstanceState(t *testing.T) {
	for state, want := range map[string]string{"running": "running", "starting": "starting", "stopped": "stopped", "failed": "failed", "": "unknown"} {
		backend := &fakeBackend{instances: []Instance{{ID: 42, State: state}}}
		if got := decode[string](t, mustCall(t, loadedPlugin(t, backend), "status", validArgs("status"))); got != want {
			t.Errorf("state %q = %q, want %q", state, got, want)
		}
	}
}

func TestInstanceStateTable(t *testing.T) {
	cases := []struct{ actual, cur, intended, want string }{
		{"running", "running", "running", "running"},
		{"", "running", "running", "starting"},
		{"loading", "running", "running", "starting"},
		{"exited", "stopped", "stopped", "stopped"},
		{"running", "stopped", "stopped", "running"}, // still winding down, still billing
		{"exited", "running", "running", "failed"},
		{"offline", "running", "running", "failed"},
		{"", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := instanceState(c.actual, c.cur, c.intended); got != c.want {
			t.Errorf("instanceState(%q,%q,%q) = %q, want %q", c.actual, c.cur, c.intended, got, c.want)
		}
	}
}

func TestUnacknowledgedWriteIsRefused(t *testing.T) {
	for op := range ackOperations {
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

// A dry run changes nothing, needs no acknowledgment and no credential, and
// never shows an env value or an onstart script.
func TestDryRunChangesNothingAndNeedsNoCredential(t *testing.T) {
	p := loadedPlugin(t, nil)
	p.backend = nil
	for op := range previewOperations {
		args := validArgs(op)
		delete(args, argAcknowledged)
		args[argDryRun] = true
		args["env"] = map[string]any{"HF_TOKEN": "hf_supersecret"}
		args["onstart"] = "export KEY=hunter2"
		got := decode[DryRunPreview](t, mustCall(t, p, op, args))
		if !got.DryRun || got.Operation != op || got.Connector != ConnectorID {
			t.Errorf("%s: preview = %+v", op, got)
		}
		raw, _ := json.Marshal(got)
		if strings.Contains(string(raw), "hf_supersecret") || strings.Contains(string(raw), "hunter2") {
			t.Errorf("%s: the preview leaked an env value or the script: %s", op, raw)
		}
	}
}

func TestCreatePreviewPricesTheOfferWhenItCan(t *testing.T) {
	backend := &fakeBackend{offers: []Offer{{ID: 7, GPUName: "RTX 6000 Ada", NumGPUs: 1, GPURAMGB: 48, DPHTotal: 0.75, DiskSpaceGB: 100}}}
	args := validArgs("create_instance")
	delete(args, argAcknowledged)
	args[argDryRun] = true
	args["max_price"] = 0.5
	got := decode[DryRunPreview](t, mustCall(t, loadedPlugin(t, backend), "create_instance", args))
	if got.Target["usd_per_day"] != 18.0 {
		t.Errorf("target = %v, want a daily price of 18", got.Target)
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "above max_price") {
		t.Errorf("warnings = %v, want the ceiling flagged", got.Warnings)
	}
	for _, m := range backend.methods() {
		if m == "CreateInstance" {
			t.Fatal("a dry run rented")
		}
	}
}

func TestDestroyPreviewShowsWhatWouldBeLost(t *testing.T) {
	backend := &fakeBackend{}
	args := map[string]any{"instance_id": float64(42), argDryRun: true}
	got := decode[DryRunPreview](t, mustCall(t, loadedPlugin(t, backend), "destroy", args))
	if got.Target["label"] != "bench" || got.Target["runtime_hours"] != 2.0 {
		t.Errorf("target = %v", got.Target)
	}
	for _, m := range backend.methods() {
		if m == "Destroy" {
			t.Fatal("a dry run destroyed")
		}
	}
}

func TestDryRunOnAnOperationWithoutOneIsRefused(t *testing.T) {
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "list_instances", map[string]any{argDryRun: true})
	code, _ := failure(t, result, err)
	if code != cerbplugin.ErrorInvalidArgs {
		t.Fatalf("code = %q", code)
	}
}

func TestMissingCredentialIsCoded(t *testing.T) {
	p := NewWithBackend(nil)
	p.backend = nil
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := callTool(p, "list_instances", nil)
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorCredentialMissing {
		t.Fatalf("code = %q", code)
	}
	if status, _ := p.Health(context.Background()); status.OK {
		t.Fatal("health should report the missing key")
	}
}

func TestErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		want cerbplugin.ErrorCode
	}{
		{&APIError{Status: 401}, cerbplugin.ErrorCredentialMissing},
		{&APIError{Status: 403, Code: "auth_error"}, cerbplugin.ErrorCredentialMissing},
		{&APIError{Status: 404, Code: "auth_error", Msg: "Invalid user key"}, cerbplugin.ErrorCredentialMissing},
		{&APIError{Status: 404, Msg: "instance 5 was not found"}, cerbplugin.ErrorInvalidArgs},
		{&APIError{Status: 400, Msg: "bad filter"}, ""},
		{&APIError{Status: 429}, ""},
	}
	for _, c := range cases {
		if got := errorCode(c.err); got != c.want {
			t.Errorf("errorCode(%+v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestGetLogsBoundsTail(t *testing.T) {
	backend := &fakeBackend{}
	got := decode[LogsResult](t, mustCall(t, loadedPlugin(t, backend), "get_logs", map[string]any{"instance_id": float64(42)}))
	if got.Logs == "" {
		t.Fatalf("logs = %+v", got)
	}
	if req := backend.calls[0].args[1].(LogsRequest); req.Tail != defaultLogTail {
		t.Errorf("tail = %d", req.Tail)
	}
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "get_logs", map[string]any{"instance_id": float64(42), "tail": float64(99999)})
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
		t.Errorf("an oversized tail should be refused, got %q", code)
	}
}

// --- cost ------------------------------------------------------------------

var costNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func costFixture() (ChargeSet, InstanceList) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	charges := ChargeSet{Charges: []Charge{
		{InstanceID: 1, Label: "bench-48gb", Start: day(12), End: day(13), Total: 3.0, GPU: 2.8, Disk: 0.2},
		{InstanceID: 2, Label: "bench-80gb", Start: day(12), End: day(13), Total: 6.0, GPU: 5.7, Disk: 0.25, Bandwidth: 0.05},
		{InstanceID: 3, Label: "bench-48gb", Start: day(13), End: day(14), Total: 1.5, GPU: 1.4, Disk: 0.1},
		{InstanceID: 4, Label: "", Start: day(13), End: day(14), Total: 0.4, GPU: 0.4},
	}}
	live := InstanceList{Instances: []Instance{
		{ID: 3, Label: "bench-48gb", State: "running", BurnPerHour: 0.75},
		{ID: 5, Label: "bench-80gb", State: "stopped", BurnPerHour: 0.02},
	}}
	return charges, live
}

func TestCostReportGroupsByWorkload(t *testing.T) {
	charges, live := costFixture()
	report := buildCostReport(costNow.AddDate(0, 0, -7), costNow, charges, live, "")

	if report.TotalUSD != 10.9 {
		t.Errorf("total = %v, want 10.9", report.TotalUSD)
	}
	// Largest first.
	names := []string{}
	for _, w := range report.ByWorkload {
		names = append(names, w.Workload)
	}
	if strings.Join(names, ",") != "bench-80gb,bench-48gb,(unlabeled)" {
		t.Fatalf("workloads = %v", names)
	}
	w80, w48 := report.ByWorkload[0], report.ByWorkload[1]
	// Instance 5 is live with no charges yet, so it still counts for bench-80gb.
	if w80.Instances != 2 || w80.TotalUSD != 6.0 || w80.LiveBurn != 0.02 {
		t.Errorf("bench-80gb = %+v", w80)
	}
	if w48.Instances != 2 || w48.TotalUSD != 4.5 || w48.GPUUSD != 4.2 || w48.LiveBurn != 0.75 {
		t.Errorf("bench-48gb = %+v", w48)
	}
	if report.BurnPerHour != 0.77 {
		t.Errorf("burn = %v, want the live 0.77", report.BurnPerHour)
	}
	notes := strings.Join(report.Notes, " ")
	if !strings.Contains(notes, "no label") {
		t.Errorf("notes = %v, want the unlabeled instance called out", report.Notes)
	}
}

func TestCostReportFiltersByWorkload(t *testing.T) {
	charges, live := costFixture()
	report := buildCostReport(costNow.AddDate(0, 0, -7), costNow, charges, live, "bench-48gb")
	if len(report.ByWorkload) != 1 || report.TotalUSD != 4.5 || len(report.Instances) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

// A destroyed instance is gone from the API but its charges remain, which is
// the reason cost is read from charges and not from the live list.
func TestCostReportKeepsDestroyedInstances(t *testing.T) {
	charges, _ := costFixture()
	report := buildCostReport(costNow.AddDate(0, 0, -7), costNow, charges, InstanceList{}, "")
	if report.TotalUSD != 10.9 || report.BurnPerHour != 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, i := range report.Instances {
		if i.Live {
			t.Errorf("instance %d reported live", i.InstanceID)
		}
	}
}

func TestCostReportOperationAddsBalanceAndRunway(t *testing.T) {
	charges, live := costFixture()
	backend := &fakeBackend{charges: charges, instances: live.Instances, account: Account{BalanceUSD: 15.4}}
	p := loadedPlugin(t, backend)
	p.now = func() time.Time { return costNow }
	got := decode[CostReport](t, mustCall(t, p, "cost_report", map[string]any{"days": float64(3)}))
	if got.BalanceUSD == nil || *got.BalanceUSD != 15.4 || got.RunwayHours == nil || *got.RunwayHours != 20 {
		t.Fatalf("balance/runway = %v / %v, want 15.4 and 20h at 0.77/hr", got.BalanceUSD, got.RunwayHours)
	}
	from := backend.calls[0].args[0].(time.Time)
	if want := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC); !from.Equal(want) {
		t.Errorf("window starts %v, want the UTC day %v", from, want)
	}
}

func TestCostReportSurvivesABalanceFailure(t *testing.T) {
	charges, live := costFixture()
	backend := &fakeBackend{charges: charges, instances: live.Instances, accountErr: errors.New("nope")}
	got := decode[CostReport](t, mustCall(t, loadedPlugin(t, backend), "cost_report", nil))
	if got.TotalUSD == 0 || got.BalanceUSD != nil || got.RunwayHours != nil {
		t.Fatalf("report = %+v", got)
	}
}

func TestCostReportBoundsDays(t *testing.T) {
	for _, days := range []any{float64(0), float64(91)} {
		result, err := callTool(loadedPlugin(t, &fakeBackend{}), "cost_report", map[string]any{"days": days})
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("days %v: code = %q", days, code)
		}
	}
}
