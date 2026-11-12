package vastaiplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "vk_live_4f9a1c7e"

// apiServer is a TLS server standing in for console.vast.ai, so the logs path
// (which refuses anything but https) is exercised as it runs for real.
func apiServer(t *testing.T, handler http.HandlerFunc) *httpBackend {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	b := newHTTPBackend(testKey, srv.URL, srv.Client())
	b.sleep = func(context.Context, time.Duration) error { return nil }
	b.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
	return b
}

func body(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("request body %q: %v", raw, err)
	}
	return out
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestEveryRequestCarriesTheKeyAsBearerAuth(t *testing.T) {
	var seen string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		reply(w, 200, map[string]any{"id": 5, "balance": 2.5, "credit": 10, "username": "Hollis Labs", "is_team": true, "email": "someone@example.com", "ssh_key": "ssh-rsa AAAA"})
	})
	account, err := b.GetAccount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer "+testKey {
		t.Fatalf("Authorization = %q", seen)
	}
	if account.UserID != 5 || account.BalanceUSD != 12.5 {
		t.Fatalf("account = %+v", account)
	}
	// The email and SSH key are not in the DTO, so cannot be returned.
	raw, _ := json.Marshal(account)
	if strings.Contains(string(raw), "example.com") || strings.Contains(string(raw), "ssh-rsa") {
		t.Fatalf("account leaked a field: %s", raw)
	}
}

func TestSearchOffersSendsTheDocumentedQuery(t *testing.T) {
	var got map[string]any
	var method, path string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, got = r.Method, r.URL.Path, body(t, r)
		reply(w, 200, map[string]any{"offers": []map[string]any{{
			"id": 111, "gpu_name": "RTX 6000Ada", "num_gpus": 1, "gpu_ram": 49140, "gpu_total_ram": 49140,
			"dph_total": 0.7421, "storage_cost": 0.15, "reliability2": 0.9921, "dlperf": 41.2,
			"dlperf_per_dphtotal": 55.5, "geolocation": "Texas, US", "cpu_cores_effective": 12.0, "cpu_ram": 128000,
			"disk_space": 200, "inet_down": 900, "inet_up": 400, "cuda_max_good": 12.4, "direct_port_count": 20,
			"verification": "verified", "duration": 604800, "machine_id": 77,
		}}})
	})
	offers, err := b.SearchOffers(context.Background(), OfferQuery{
		GPUNames: []string{"RTX 6000Ada"}, MinVRAMGB: 48, MaxDPH: 1, MinReliability: 0.98, Limit: 5, OrderBy: "dph_total",
	})
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/api/v0/bundles/" {
		t.Fatalf("%s %s", method, path)
	}
	if got["rentable"].(map[string]any)["eq"] != true || got["type"] != "ondemand" || got["limit"] != float64(5) {
		t.Errorf("body = %v", got)
	}
	if got["verified"].(map[string]any)["eq"] != true {
		t.Errorf("unverified machines should be excluded by default: %v", got["verified"])
	}
	// A 48 GB card lists 49140 MB, so the floor is 48000, not 48 x 1024.
	if got["gpu_ram"].(map[string]any)["gte"] != float64(48000) {
		t.Errorf("gpu_ram filter = %v", got["gpu_ram"])
	}
	if order := got["order"].([]any)[0].([]any); order[0] != "dph_total" || order[1] != "asc" {
		t.Errorf("order = %v", got["order"])
	}

	o := offers[0]
	if o.ID != 111 || o.GPURAMGB != 48 || o.TotalVRAMGB != 48 || o.Reliability != 0.9921 || !o.Verified || o.CPURAMGB != 125 {
		t.Errorf("offer = %+v", o)
	}
	if o.DPHPerGPU != 0.7421 || o.USDPerVRAMGBHour != round(0.7421/48, 5) {
		t.Errorf("derived prices = %v, %v", o.DPHPerGPU, o.USDPerVRAMGBHour)
	}
}

func TestUnverifiedSearchWidensTheFilter(t *testing.T) {
	body := offerBody(OfferQuery{Unverified: true})
	if in := body["verified"].(map[string]any)["in"].([]bool); len(in) != 2 {
		t.Fatalf("verified filter = %v", body["verified"])
	}
}

func TestListInstancesWalksEveryPageAndDecodes(t *testing.T) {
	var requests int32
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/api/v1/instances/" {
			t.Errorf("path = %s", r.URL.Path)
		}
		switch r.URL.Query().Get("after_token") {
		case "":
			reply(w, 200, map[string]any{"success": true, "next_token": "page2", "instances": []map[string]any{{
				"id": 2, "label": "bench-80gb", "actual_status": "running", "cur_state": "running", "intended_status": "running",
				"gpu_name": "H100 SXM", "num_gpus": 1, "gpu_ram": 81559, "image_uuid": "vllm/vllm-openai:latest", "disk_space": 100,
				"ssh_host": "ssh7.vast.ai", "ssh_port": 10600, "public_ipaddr": "203.0.113.9",
				"ports":     map[string]any{"8000/tcp": []map[string]any{{"HostIp": "0.0.0.0", "HostPort": "41022"}}},
				"dph_total": 2.1021, "storage_total_cost": 0.0021, "start_date": 1763121600.5,
				// Fields that must not leave the plugin.
				"jupyter_token": "jt_secret", "extra_env": [][]string{{"HF_TOKEN", "hf_secret"}}, "onstart": "export X=1",
			}}})
		case "page2":
			reply(w, 200, map[string]any{"success": true, "next_token": nil, "instances": []map[string]any{{
				"id": 1, "label": "", "actual_status": "exited", "cur_state": "stopped", "intended_status": "stopped",
				"gpu_name": "RTX 4090", "num_gpus": 1, "gpu_ram": 24564, "dph_total": 0.4021, "storage_total_cost": 0.0021, "start_date": nil,
			}}})
		default:
			t.Errorf("unexpected token %q", r.URL.Query().Get("after_token"))
		}
	})
	list, err := b.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || list.Count != 2 || list.Truncated {
		t.Fatalf("requests=%d list=%+v", requests, list)
	}
	// Sorted by id, whatever the page order.
	stopped, running := list.Instances[0], list.Instances[1]
	if stopped.ID != 1 || stopped.State != "stopped" || stopped.BurnPerHour != 0.0021 {
		t.Errorf("stopped instance = %+v; a stopped instance bills the disk alone", stopped)
	}
	if running.State != "running" || running.BurnPerHour != 2.1021 || running.SSHPort != 10600 || running.Ports["8000/tcp"] != "41022" {
		t.Errorf("running instance = %+v", running)
	}
	// 2026-11-14T12:00Z minus the start of 1763121600 (2025-11-14T12:00Z) is 365 days.
	if running.RuntimeHours != 8760 || running.EstimatedCost != round(8760*2.1021, 2) {
		t.Errorf("runtime = %v h, estimate = %v", running.RuntimeHours, running.EstimatedCost)
	}
	if list.BurnPerHour != round(2.1021+0.0021, 4) {
		t.Errorf("burn = %v", list.BurnPerHour)
	}
	raw, _ := json.Marshal(list)
	for _, secret := range []string{"jt_secret", "hf_secret", "export X=1"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the listing leaked %q: %s", secret, raw)
		}
	}
}

func TestGetInstanceFiltersByIDAndReports404(t *testing.T) {
	var filter string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		filter = r.URL.Query().Get("select_filters")
		reply(w, 200, map[string]any{"success": true, "instances": []any{}})
	})
	_, err := b.GetInstance(context.Background(), 31)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %v, want a 404 APIError", err)
	}
	if !strings.Contains(filter, `"id":{"eq":31}`) {
		t.Errorf("select_filters = %s", filter)
	}
	if errorCode(err) == "" {
		t.Error("an unknown instance should be invalid_args")
	}
}

func TestCreateInstanceSendsTheRentalAndDropsTheInstanceKey(t *testing.T) {
	var got map[string]any
	var path, method string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		method, path, got = r.Method, r.URL.Path, body(t, r)
		reply(w, 200, map[string]any{"success": true, "new_contract": 5550001, "instance_api_key": "d15a-container-credential"})
	})
	id, err := b.CreateInstance(context.Background(), CreateRequest{
		OfferID: 111, Image: "vllm/vllm-openai:latest", Label: "bench-48gb", DiskGB: 80,
		Onstart: "nvidia-smi", Env: map[string]string{"-p 8000:8000": "1"}, TargetState: "stopped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != 5550001 || method != http.MethodPut || path != "/api/v0/asks/111/" {
		t.Fatalf("id=%d %s %s", id, method, path)
	}
	if got["image"] != "vllm/vllm-openai:latest" || got["label"] != "bench-48gb" || got["disk"] != float64(80) || got["target_state"] != "stopped" {
		t.Errorf("body = %v", got)
	}
	if got["env"].(map[string]any)["-p 8000:8000"] != "1" {
		t.Errorf("env = %v", got["env"])
	}
}

func TestStateLabelAndDestroyUseTheDocumentedCalls(t *testing.T) {
	type seen struct {
		method, path string
		body         map[string]any
	}
	var calls []seen
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		s := seen{method: r.Method, path: r.URL.Path}
		if r.Method == http.MethodPut {
			s.body = body(t, r)
		}
		calls = append(calls, s)
		reply(w, 200, map[string]any{"success": true})
	})
	ctx := context.Background()
	for _, err := range []error{b.SetState(ctx, 9, "stopped"), b.SetLabel(ctx, 9, "bench"), b.Destroy(ctx, 9)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls[0].method != "PUT" || calls[0].path != "/api/v0/instances/9/" || calls[0].body["state"] != "stopped" {
		t.Errorf("stop = %+v", calls[0])
	}
	if calls[1].body["label"] != "bench" {
		t.Errorf("label = %+v", calls[1])
	}
	if calls[2].method != "DELETE" || calls[2].path != "/api/v0/instances/9/" {
		t.Errorf("destroy = %+v", calls[2])
	}
}

func TestChargesAreSummedByType(t *testing.T) {
	var query map[string]string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		query = map[string]string{"filters": r.URL.Query().Get("select_filters"), "format": r.URL.Query().Get("format")}
		reply(w, 200, map[string]any{"success": true, "next_token": nil, "results": []map[string]any{{
			"start": 1763000000, "end": 1763400000, "type": "instance", "source": "instance-5550001", "amount": 3.5,
			"metadata": map[string]any{"label": "bench-48gb"},
			"items": []map[string]any{
				{"type": "gpu", "amount": 3.0}, {"type": "disk", "amount": 0.4}, {"type": "bwd", "amount": 0.06}, {"type": "bwu", "amount": 0.04},
			},
		}, {
			"start": 1763000000, "end": 1763400000, "type": "volume", "source": "volume-9", "amount": 9.0,
		}}})
	})
	from := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	set, err := b.Charges(context.Background(), from, from.AddDate(0, 0, 7))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Charges) != 1 {
		t.Fatalf("charges = %+v; a volume charge is not an instance's", set.Charges)
	}
	c := set.Charges[0]
	if c.InstanceID != 5550001 || c.Label != "bench-48gb" || c.Total != 3.5 || c.GPU != 3.0 || c.Disk != 0.4 || c.Bandwidth != 0.1 {
		t.Errorf("charge = %+v", c)
	}
	if !strings.Contains(query["filters"], `"day":{"gte":`) || !strings.Contains(query["filters"], `"in":["instance"]`) || query["format"] != "table" {
		t.Errorf("query = %v", query)
	}
}

func TestChargesStopAtThePageCap(t *testing.T) {
	var n int32
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		reply(w, 200, map[string]any{"success": true, "next_token": "more", "results": []map[string]any{
			{"type": "instance", "source": "instance-1", "amount": 1},
		}})
	})
	set, err := b.Charges(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !set.Truncated || int(n) != maxChargePages {
		t.Fatalf("truncated=%v after %d pages, want a stop at %d", set.Truncated, n, maxChargePages)
	}
}

func TestRateLimitIsRetriedThenSucceeds(t *testing.T) {
	var n int32
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) < 3 {
			reply(w, 429, map[string]any{"detail": "API requests too frequent endpoint threshold=1.0"})
			return
		}
		reply(w, 200, map[string]any{"id": 1, "balance": 1})
	})
	if _, err := b.GetAccount(context.Background()); err != nil || n != 3 {
		t.Fatalf("err=%v after %d attempts", err, n)
	}
}

func TestRateLimitGivesUpAfterTheRetries(t *testing.T) {
	var n int32
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		reply(w, 429, map[string]any{"detail": "too frequent"})
	})
	_, err := b.GetAccount(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 429 || int(n) != maxRetries+1 {
		t.Fatalf("err=%v after %d attempts", err, n)
	}
}

// Vast.ai answers some failures with a 200 whose body says success is false.
func TestSuccessFalseIsAnError(t *testing.T) {
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"success": false, "error": "insufficient_credit", "msg": "Insufficient credit to rent this offer"})
	})
	err := b.SetState(context.Background(), 1, "running")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !strings.Contains(err.Error(), "Insufficient credit") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthErrorsAreCredentialErrors(t *testing.T) {
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 404, map[string]any{"success": false, "error": "auth_error", "msg": "Invalid user key"})
	})
	_, err := b.GetAccount(context.Background())
	if errorCode(err) != "credential_missing" {
		t.Fatalf("code = %q for %v", errorCode(err), err)
	}
}

func TestLogsPollUntilTheUploadLands(t *testing.T) {
	var hits int32
	var storageAuth string
	var srvURL string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v0/instances/request_logs/"):
			got := body(t, r)
			if got["tail"] != "50" || got["filter"] != "ERROR" || got["daemon_logs"] != "true" {
				t.Errorf("log request = %v", got)
			}
			reply(w, 200, map[string]any{"success": true, "result_url": srvURL + "/logs/abc.log"})
		case r.URL.Path == "/logs/abc.log":
			storageAuth = r.Header.Get("Authorization")
			if atomic.AddInt32(&hits, 1) < 3 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, "starting\nready\n")
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	srvURL = b.base
	out, err := b.Logs(context.Background(), 12, LogsRequest{Tail: 50, Filter: "ERROR", Daemon: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Logs != "starting\nready\n" || hits != 3 || out.InstanceID != 12 {
		t.Fatalf("logs = %+v after %d fetches", out, hits)
	}
	// The log lives at a pre-signed storage URL: the API key must not go there.
	if storageAuth != "" {
		t.Fatalf("the key was sent to the storage URL: %q", storageAuth)
	}
}

func TestLogsRefuseANonHTTPSLocation(t *testing.T) {
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"success": true, "result_url": "http://internal.example/secret"})
	})
	if _, err := b.Logs(context.Background(), 1, LogsRequest{Tail: 10}); err == nil {
		t.Fatal("a plain-http log location should be refused")
	}
}

func TestLogsAreCappedAtTheirTail(t *testing.T) {
	var srvURL string
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			reply(w, 200, map[string]any{"success": true, "result_url": srvURL + "/big"})
			return
		}
		fmt.Fprint(w, strings.Repeat("x", maxLogBytes+500)+"END")
	})
	srvURL = b.base
	out, err := b.Logs(context.Background(), 1, LogsRequest{Tail: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || len(out.Logs) != maxLogBytes || !strings.HasSuffix(out.Logs, "END") {
		t.Fatalf("truncated=%v len=%d: want the last %d bytes", out.Truncated, len(out.Logs), maxLogBytes)
	}
}

func TestNumbersMayArriveAsStringsOrNull(t *testing.T) {
	var r rawInstance
	if err := json.Unmarshal([]byte(`{"id":"7","dph_total":"0.5","gpu_ram":null,"ssh_port":"22"}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != 7 || r.DPHTotal != 0.5 || r.GPURAM != 0 || r.SSHPort != 22 {
		t.Fatalf("decoded = %+v", r)
	}
}

func TestGetOfferFiltersOnAskContractID(t *testing.T) {
	var got map[string]any
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = body(t, r)
		reply(w, 200, map[string]any{"offers": []map[string]any{{"id": 53565126, "gpu_name": "RTX A6000", "num_gpus": 1, "gpu_ram": 49140, "dph_total": 0.4689}}})
	})
	o, err := b.GetOffer(context.Background(), 53565126)
	if err != nil || o == nil || o.ID != 53565126 {
		t.Fatalf("offer = %+v, err = %v", o, err)
	}
	// "id" is silently ignored by the live API, so it must not be the filter.
	if _, bad := got["id"]; bad {
		t.Errorf("body filters on id: %v", got)
	}
	if got["ask_contract_id"].(map[string]any)["eq"] != float64(53565126) {
		t.Errorf("body = %v", got)
	}
}

func TestAccountBalanceIsCreditPlusBalance(t *testing.T) {
	// A funded prepaid team account reports balance 0 and the money as credit.
	b := apiServer(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"id": 7, "balance": 0, "credit": 25.0, "username": "Example Team", "is_team": true})
	})
	a, err := b.GetAccount(context.Background())
	if err != nil || a.BalanceUSD != 25 || !a.IsTeam {
		t.Fatalf("account = %+v, err = %v", a, err)
	}
}
