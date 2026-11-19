package vastaiplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Vast.ai's API host.
const DefaultBaseURL = "https://console.vast.ai"

// Bounds that keep one call from holding the plugin open forever.
const (
	maxResponseBytes = 8 << 20 // an API response
	maxLogBytes      = 256 << 10
	maxInstancePages = 40 // 25 per page
	maxChargePages   = 20 // 500 per page
	maxRetries       = 3  // on 429 only
	instancePageSize = 25
	chargePageSize   = 500
	logFetchAttempts = 12 // the log file is uploaded asynchronously
)

// APIError is a non-2xx answer, or a 2xx whose body says success is false. Its
// text names the call and the API's own message; it never carries the request,
// which can hold an env block or an onstart script.
type APIError struct {
	Method string
	Path   string
	Status int
	Code   string // the API's "error" field, e.g. auth_error
	Msg    string
}

func (e *APIError) Error() string {
	msg := e.Msg
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("vast.ai %s %s: %d %s", e.Method, e.Path, e.Status, msg)
}

// httpBackend is the only code in this plugin that speaks Vast.ai's wire
// format. Every value leaving it has been decoded into an explicit struct and
// mapped onto a DTO by a function below; those functions are the security
// boundary and are meant to read like one.
type httpBackend struct {
	base  string
	key   string
	http  *http.Client
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// NewHTTPBackend builds the live backend. The key is passed in rather than
// read here: the host resolves it and hands it over at init.
func NewHTTPBackend(apiKey string) Backend {
	return newHTTPBackend(apiKey, DefaultBaseURL, &http.Client{Timeout: 60 * time.Second})
}

// newHTTPBackend is the test seam for a client pointed at a local server.
func newHTTPBackend(apiKey, base string, client *http.Client) *httpBackend {
	return &httpBackend{
		base:  strings.TrimRight(base, "/"),
		key:   apiKey,
		http:  client,
		sleep: sleepCtx,
		now:   time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends one request and decodes the JSON answer into out (nil to discard).
// A 429 is retried: Vast.ai rate-limits per endpoint, and a 429 is refused
// before it is processed, so retrying a create cannot rent twice.
func (b *httpBackend) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	target := b.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	for attempt := 0; ; attempt++ {
		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+b.key)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := b.http.Do(req)
		if err != nil {
			// *url.Error repeats the URL, which for a GET holds filters but
			// never the key; the scrubber covers the rest.
			return fmt.Errorf("vast.ai %s %s: %w", method, path, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("vast.ai %s %s: read response: %w", method, path, readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			wait := retryDelay(resp.Header.Get("Retry-After"), attempt)
			if err := b.sleep(ctx, wait); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return apiError(method, path, resp.StatusCode, raw)
		}
		// Vast.ai sometimes answers 200 with {"success": false, "msg": ...}.
		var status struct {
			Success *bool  `json:"success"`
			Error   string `json:"error"`
			Msg     string `json:"msg"`
		}
		if json.Unmarshal(raw, &status) == nil && status.Success != nil && !*status.Success {
			return &APIError{Method: method, Path: path, Status: resp.StatusCode, Code: status.Error, Msg: status.Msg}
		}
		if out == nil || len(bytes.TrimSpace(raw)) == 0 {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("vast.ai %s %s: unreadable response: %w", method, path, err)
		}
		return nil
	}
}

func retryDelay(header string, attempt int) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs > 0 {
		return time.Duration(min(secs, 10)) * time.Second
	}
	return time.Duration(1<<attempt) * time.Second
}

func apiError(method, path string, status int, raw []byte) error {
	e := &APIError{Method: method, Path: path, Status: status}
	var body struct {
		Error  string `json:"error"`
		Msg    string `json:"msg"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &body) == nil {
		e.Code, e.Msg = body.Error, body.Msg
		if e.Msg == "" {
			e.Msg = body.Detail
		}
	}
	return e
}

// num reads a JSON number that Vast.ai sometimes sends as a string or null.
type num float64

func (n *num) UnmarshalJSON(data []byte) error {
	s := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %q", s)
	}
	*n = num(f)
	return nil
}

func (n num) f() float64 { return float64(n) }

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// mbToGB converts the API's MB to the GB a GPU is sold as: 24576 MB is a
// 24 GB card.
func mbToGB(mb float64) float64 { return round(mb/1024, 1) }

// --- account ---------------------------------------------------------------

func (b *httpBackend) GetAccount(ctx context.Context) (*Account, error) {
	var raw struct {
		ID       num    `json:"id"`
		Balance  num    `json:"balance"`
		Credit   num    `json:"credit"`
		Username string `json:"username"`
		IsTeam   bool   `json:"is_team"`
	}
	if err := b.do(ctx, http.MethodGet, "/api/v0/users/current/", nil, nil, &raw); err != nil {
		return nil, err
	}
	// Spendable funds are credit plus balance. A prepaid (credit-only) account,
	// which is what a team account funded by card is, holds its money in
	// "credit" and reports "balance": 0; reading balance alone showed $0 on a
	// funded account. A negative balance is a debt and correctly reduces it.
	return &Account{
		UserID:     int(raw.ID),
		Username:   raw.Username,
		IsTeam:     raw.IsTeam,
		BalanceUSD: round(raw.Balance.f()+raw.Credit.f(), 4),
	}, nil
}

// --- offers ----------------------------------------------------------------

type rawOffer struct {
	ID              num    `json:"id"`
	GPUName         string `json:"gpu_name"`
	NumGPUs         num    `json:"num_gpus"`
	GPURAM          num    `json:"gpu_ram"`
	GPUTotalRAM     num    `json:"gpu_total_ram"`
	DPHTotal        num    `json:"dph_total"`
	StorageCost     num    `json:"storage_cost"`
	Reliability     num    `json:"reliability"`
	Reliability2    num    `json:"reliability2"`
	DLPerf          num    `json:"dlperf"`
	DLPerfPerDPH    num    `json:"dlperf_per_dphtotal"`
	Geolocation     string `json:"geolocation"`
	CPUCoresEff     num    `json:"cpu_cores_effective"`
	CPURAM          num    `json:"cpu_ram"`
	DiskSpace       num    `json:"disk_space"`
	InetDown        num    `json:"inet_down"`
	InetUp          num    `json:"inet_up"`
	CUDAMax         num    `json:"cuda_max_good"`
	DirectPortCount num    `json:"direct_port_count"`
	Verification    string `json:"verification"`
	Duration        num    `json:"duration"`
	MinBid          num    `json:"min_bid"`
	MachineID       num    `json:"machine_id"`
}

func offerFromAPI(r rawOffer) Offer {
	gpus := int(r.NumGPUs)
	perGPU := mbToGB(r.GPURAM.f())
	total := mbToGB(r.GPUTotalRAM.f())
	if total == 0 {
		total = round(perGPU*float64(gpus), 1)
	}
	reliability := r.Reliability.f()
	if reliability == 0 {
		reliability = r.Reliability2.f()
	}
	o := Offer{
		ID:               int(r.ID),
		GPUName:          r.GPUName,
		NumGPUs:          gpus,
		GPURAMGB:         perGPU,
		TotalVRAMGB:      total,
		DPHTotal:         round(r.DPHTotal.f(), 4),
		StoragePerGBMo:   round(r.StorageCost.f(), 4),
		Reliability:      round(reliability, 4),
		DLPerf:           round(r.DLPerf.f(), 2),
		DLPerfPerDPH:     round(r.DLPerfPerDPH.f(), 2),
		Geolocation:      r.Geolocation,
		CPUCores:         round(r.CPUCoresEff.f(), 1),
		CPURAMGB:         mbToGB(r.CPURAM.f()),
		DiskSpaceGB:      round(r.DiskSpace.f(), 1),
		InetDownMbps:     round(r.InetDown.f(), 1),
		InetUpMbps:       round(r.InetUp.f(), 1),
		CUDAMax:          r.CUDAMax.f(),
		DirectPorts:      int(r.DirectPortCount),
		Verified:         r.Verification == "verified",
		AvailableSeconds: round(r.Duration.f(), 0),
		MinBid:           round(r.MinBid.f(), 4),
		MachineID:        int(r.MachineID),
	}
	if gpus > 0 {
		o.DPHPerGPU = round(o.DPHTotal/float64(gpus), 4)
	}
	if total > 0 {
		o.USDPerVRAMGBHour = round(o.DPHTotal/total, 5)
	}
	return o
}

// offerBody builds the search_offers request body from a query.
func offerBody(q OfferQuery) map[string]any {
	body := map[string]any{
		"rentable": map[string]any{"eq": true},
		"rented":   map[string]any{"eq": false},
		"type":     orDefault(q.Type, "ondemand"),
		"limit":    q.Limit,
	}
	if q.Unverified {
		body["verified"] = map[string]any{"in": []bool{true, false}}
	} else {
		body["verified"] = map[string]any{"eq": true}
	}
	if len(q.GPUNames) > 0 {
		body["gpu_name"] = map[string]any{"in": q.GPUNames}
	}
	if q.NumGPUs > 0 {
		body["num_gpus"] = map[string]any{"eq": q.NumGPUs}
	} else if q.MinGPUs > 0 {
		body["num_gpus"] = map[string]any{"gte": q.MinGPUs}
	}
	// The API filters GPU RAM in MB. A "48 GB" card lists as 49140 MB and an
	// "80 GB" one as 81559 to 81920, so the cut is GB x 1000, not x 1024.
	if q.MinVRAMGB > 0 {
		body["gpu_ram"] = map[string]any{"gte": q.MinVRAMGB * 1000}
	}
	if q.MinTotalVRAMGB > 0 {
		body["gpu_total_ram"] = map[string]any{"gte": q.MinTotalVRAMGB * 1000}
	}
	if q.MaxDPH > 0 {
		body["dph_total"] = map[string]any{"lte": q.MaxDPH}
	}
	if q.MinReliability > 0 {
		body["reliability"] = map[string]any{"gte": q.MinReliability}
	}
	if q.MinDiskGB > 0 {
		body["disk_space"] = map[string]any{"gte": q.MinDiskGB}
	}
	if q.MinInetDown > 0 {
		body["inet_down"] = map[string]any{"gte": q.MinInetDown}
	}
	if len(q.Geolocation) > 0 {
		body["geolocation"] = map[string]any{"in": q.Geolocation}
	}
	dir := "asc"
	if q.Descending {
		dir = "desc"
	}
	body["order"] = [][]string{{orDefault(q.OrderBy, "dph_total"), dir}}
	return body
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (b *httpBackend) SearchOffers(ctx context.Context, q OfferQuery) ([]Offer, error) {
	var resp struct {
		Offers []rawOffer `json:"offers"`
	}
	if err := b.do(ctx, http.MethodPost, "/api/v0/bundles/", nil, offerBody(q), &resp); err != nil {
		return nil, err
	}
	offers := make([]Offer, 0, len(resp.Offers))
	for _, r := range resp.Offers {
		offers = append(offers, offerFromAPI(r))
	}
	return offers, nil
}

func (b *httpBackend) GetOffer(ctx context.Context, id int) (*Offer, error) {
	// The bundles endpoint ignores an "id" filter (it matches nothing); the
	// offer id is the ask contract id. Checked live against the real API.
	body := map[string]any{
		"ask_contract_id": map[string]any{"eq": id},
		"rentable":        map[string]any{"eq": true},
		"rented":          map[string]any{"eq": false},
		"verified":        map[string]any{"in": []bool{true, false}},
		"type":            "ondemand",
		"limit":           1,
	}
	var resp struct {
		Offers []rawOffer `json:"offers"`
	}
	if err := b.do(ctx, http.MethodPost, "/api/v0/bundles/", nil, body, &resp); err != nil {
		return nil, err
	}
	for _, r := range resp.Offers {
		if int(r.ID) == id {
			o := offerFromAPI(r)
			return &o, nil
		}
	}
	return nil, nil
}

// --- instances -------------------------------------------------------------

type rawInstance struct {
	ID               num             `json:"id"`
	Label            string          `json:"label"`
	ActualStatus     string          `json:"actual_status"`
	CurState         string          `json:"cur_state"`
	IntendedStatus   string          `json:"intended_status"`
	StatusMsg        string          `json:"status_msg"`
	GPUName          string          `json:"gpu_name"`
	NumGPUs          num             `json:"num_gpus"`
	GPURAM           num             `json:"gpu_ram"`
	ImageUUID        string          `json:"image_uuid"`
	DiskSpace        num             `json:"disk_space"`
	Geolocation      string          `json:"geolocation"`
	MachineID        num             `json:"machine_id"`
	IsBid            bool            `json:"is_bid"`
	SSHHost          string          `json:"ssh_host"`
	SSHPort          num             `json:"ssh_port"`
	PublicIP         string          `json:"public_ipaddr"`
	Ports            json.RawMessage `json:"ports"`
	DPHTotal         num             `json:"dph_total"`
	StorageTotalCost num             `json:"storage_total_cost"`
	StartDate        num             `json:"start_date"`
}

func (b *httpBackend) instanceFromAPI(r rawInstance) Instance {
	state := string(instanceState(r.ActualStatus, r.CurState, r.IntendedStatus))
	i := Instance{
		ID:            int(r.ID),
		Label:         r.Label,
		State:         state,
		ActualStatus:  r.ActualStatus,
		IntendedState: r.IntendedStatus,
		StatusMsg:     r.StatusMsg,
		GPUName:       r.GPUName,
		NumGPUs:       int(r.NumGPUs),
		GPURAMGB:      mbToGB(r.GPURAM.f()),
		Image:         r.ImageUUID,
		DiskGB:        round(r.DiskSpace.f(), 1),
		Geolocation:   r.Geolocation,
		MachineID:     int(r.MachineID),
		IsBid:         r.IsBid,
		SSHHost:       r.SSHHost,
		SSHPort:       int(r.SSHPort),
		PublicIP:      r.PublicIP,
		Ports:         portsFromAPI(r.Ports),
		DPHTotal:      round(r.DPHTotal.f(), 4),
		StorageDPH:    round(r.StorageTotalCost.f(), 4),
	}
	// dph_total includes the disk. A stopped instance stops paying for the GPU
	// but not for the disk, so its burn is the disk alone.
	i.BurnPerHour = i.DPHTotal
	if state == "stopped" {
		i.BurnPerHour = i.StorageDPH
	}
	if start := r.StartDate.f(); start > 0 {
		started := time.Unix(int64(start), 0).UTC()
		i.StartedAt = &started
		if hours := b.now().Sub(started).Hours(); hours > 0 {
			i.RuntimeHours = round(hours, 2)
			i.EstimatedCost = round(hours*i.DPHTotal, 2)
		}
	}
	return i
}

// instanceState normalizes Vast.ai's three status fields to a Cerberus
// resource state. actual_status is what the container is doing, cur_state what
// the instance is set to, intended_status what was last asked for.
func instanceState(actual, cur, intended string) string {
	switch {
	case cur == "running" && actual == "running":
		return "running"
	case cur == "running":
		// Provisioning (actual null), pulling the image (loading), or created.
		if actual == "exited" || actual == "offline" || actual == "unknown" {
			return "failed"
		}
		return "starting"
	case cur == "stopped" && actual == "running":
		// Asked to stop, still winding down: it is still billing as running.
		return "running"
	case cur == "stopped" || intended == "stopped":
		return "stopped"
	}
	return "unknown"
}

// portsFromAPI reads Docker-style port bindings, {"8000/tcp":[{"HostPort":"41"}]},
// down to container port -> public port. Anything else yields nothing.
func portsFromAPI(raw json.RawMessage) map[string]string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string][]struct {
		HostPort string `json:"HostPort"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := map[string]string{}
	for port, bindings := range m {
		if len(bindings) > 0 && bindings[0].HostPort != "" {
			out[port] = bindings[0].HostPort
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type instancePage struct {
	NextToken string        `json:"next_token"`
	Instances []rawInstance `json:"instances"`
}

func (b *httpBackend) listInstancesPage(ctx context.Context, filters map[string]any, token string) (instancePage, error) {
	query := url.Values{"limit": {strconv.Itoa(instancePageSize)}}
	if len(filters) > 0 {
		encoded, _ := json.Marshal(filters)
		query.Set("select_filters", string(encoded))
	}
	if token != "" {
		query.Set("after_token", token)
	}
	var page instancePage
	err := b.do(ctx, http.MethodGet, "/api/v1/instances/", query, nil, &page)
	return page, err
}

func (b *httpBackend) ListInstances(ctx context.Context) (InstanceList, error) {
	out := InstanceList{Instances: []Instance{}}
	token := ""
	for pages := 0; ; pages++ {
		page, err := b.listInstancesPage(ctx, nil, token)
		if err != nil {
			return InstanceList{}, err
		}
		for _, r := range page.Instances {
			out.Instances = append(out.Instances, b.instanceFromAPI(r))
		}
		// Stop on the last page, and on an empty one: a response that claims
		// more pages but returns nothing must not loop forever.
		if page.NextToken == "" || len(page.Instances) == 0 {
			break
		}
		if pages+1 >= maxInstancePages {
			out.Truncated = true
			break
		}
		token = page.NextToken
	}
	sort.Slice(out.Instances, func(i, j int) bool { return out.Instances[i].ID < out.Instances[j].ID })
	out.Count = len(out.Instances)
	for _, i := range out.Instances {
		out.BurnPerHour += i.BurnPerHour
	}
	out.BurnPerHour = round(out.BurnPerHour, 4)
	return out, nil
}

func (b *httpBackend) GetInstance(ctx context.Context, id int) (*Instance, error) {
	page, err := b.listInstancesPage(ctx, map[string]any{"id": map[string]any{"eq": id}}, "")
	if err != nil {
		return nil, err
	}
	for _, r := range page.Instances {
		if int(r.ID) == id {
			i := b.instanceFromAPI(r)
			return &i, nil
		}
	}
	return nil, &APIError{Method: http.MethodGet, Path: "/api/v1/instances/", Status: http.StatusNotFound,
		Msg: fmt.Sprintf("instance %d was not found", id)}
}

func (b *httpBackend) CreateInstance(ctx context.Context, req CreateRequest) (int, error) {
	body := map[string]any{
		"image": req.Image,
		"label": req.Label,
	}
	if req.DiskGB > 0 {
		body["disk"] = req.DiskGB
	}
	if req.Onstart != "" {
		body["onstart"] = req.Onstart
	}
	if len(req.Env) > 0 {
		body["env"] = req.Env
	}
	if req.Runtype != "" {
		body["runtype"] = req.Runtype
	}
	if req.TargetState != "" {
		body["target_state"] = req.TargetState
	}
	// The response also carries instance_api_key, a credential injected into
	// the container. It is decoded into nothing and so cannot be returned.
	var resp struct {
		NewContract num `json:"new_contract"`
	}
	path := fmt.Sprintf("/api/v0/asks/%d/", req.OfferID)
	if err := b.do(ctx, http.MethodPut, path, nil, body, &resp); err != nil {
		return 0, err
	}
	if resp.NewContract == 0 {
		return 0, errors.New("vast.ai returned no instance id for the rental")
	}
	return int(resp.NewContract), nil
}

func (b *httpBackend) SetState(ctx context.Context, id int, state string) error {
	return b.do(ctx, http.MethodPut, fmt.Sprintf("/api/v0/instances/%d/", id), nil, map[string]any{"state": state}, nil)
}

func (b *httpBackend) SetLabel(ctx context.Context, id int, label string) error {
	return b.do(ctx, http.MethodPut, fmt.Sprintf("/api/v0/instances/%d/", id), nil, map[string]any{"label": label}, nil)
}

func (b *httpBackend) Destroy(ctx context.Context, id int) error {
	return b.do(ctx, http.MethodDelete, fmt.Sprintf("/api/v0/instances/%d/", id), nil, nil, nil)
}

// --- charges ---------------------------------------------------------------

type rawCharge struct {
	Start    num    `json:"start"`
	End      num    `json:"end"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Amount   num    `json:"amount"`
	Metadata struct {
		Label string `json:"label"`
	} `json:"metadata"`
	Items []struct {
		Type   string `json:"type"`
		Amount num    `json:"amount"`
	} `json:"items"`
}

func (b *httpBackend) Charges(ctx context.Context, from, to time.Time) (ChargeSet, error) {
	filters := map[string]any{
		"day":  map[string]any{"gte": from.Unix(), "lte": to.Unix()},
		"type": map[string]any{"in": []string{"instance"}},
	}
	encoded, _ := json.Marshal(filters)
	out := ChargeSet{}
	token := ""
	for pages := 0; ; pages++ {
		query := url.Values{
			"select_filters": {string(encoded)},
			"format":         {"table"},
			"limit":          {strconv.Itoa(chargePageSize)},
		}
		if token != "" {
			query.Set("after_token", token)
		}
		var page struct {
			NextToken string      `json:"next_token"`
			Results   []rawCharge `json:"results"`
		}
		if err := b.do(ctx, http.MethodGet, "/api/v0/charges/", query, nil, &page); err != nil {
			return ChargeSet{}, err
		}
		for _, r := range page.Results {
			if r.Type != "" && r.Type != "instance" {
				continue
			}
			c := Charge{
				InstanceID: sourceID(r.Source),
				Label:      r.Metadata.Label,
				Start:      time.Unix(int64(r.Start.f()), 0).UTC(),
				End:        time.Unix(int64(r.End.f()), 0).UTC(),
				Total:      r.Amount.f(),
			}
			for _, item := range r.Items {
				switch item.Type {
				case "gpu":
					c.GPU += item.Amount.f()
				case "disk":
					c.Disk += item.Amount.f()
				case "bwd", "bwu":
					c.Bandwidth += item.Amount.f()
				}
			}
			out.Charges = append(out.Charges, c)
		}
		if page.NextToken == "" || len(page.Results) == 0 {
			break
		}
		if pages+1 >= maxChargePages {
			out.Truncated = true
			break
		}
		token = page.NextToken
	}
	return out, nil
}

// sourceID reads the instance id from a charge source like "instance-12345".
func sourceID(source string) int {
	_, id, ok := strings.Cut(source, "-")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(id)
	if err != nil {
		return 0
	}
	return n
}

// --- logs ------------------------------------------------------------------

// Logs asks Vast.ai to upload the instance's log to a URL, then fetches it.
// The upload is asynchronous, so the first fetches can find nothing yet.
func (b *httpBackend) Logs(ctx context.Context, id int, req LogsRequest) (LogsResult, error) {
	body := map[string]any{"tail": strconv.Itoa(req.Tail)}
	if req.Filter != "" {
		body["filter"] = req.Filter
	}
	if req.Daemon {
		body["daemon_logs"] = "true"
	}
	var resp struct {
		ResultURL string `json:"result_url"`
	}
	if err := b.do(ctx, http.MethodPut, fmt.Sprintf("/api/v0/instances/request_logs/%d/", id), nil, body, &resp); err != nil {
		return LogsResult{}, err
	}
	logURL, err := url.Parse(resp.ResultURL)
	if err != nil || logURL.Scheme != "https" || logURL.Host == "" {
		return LogsResult{}, errors.New("vast.ai returned no usable log location")
	}
	var lastStatus int
	for attempt := 0; attempt < logFetchAttempts; attempt++ {
		if attempt > 0 {
			if err := b.sleep(ctx, time.Second); err != nil {
				return LogsResult{}, err
			}
		}
		fetch, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
		if err != nil {
			return LogsResult{}, err
		}
		// No Authorization header: this is a pre-signed storage URL, not the
		// Vast.ai API, and the key must not be sent to it.
		res, err := b.http.Do(fetch)
		if err != nil {
			return LogsResult{}, fmt.Errorf("fetch instance %d logs: %w", id, err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
		res.Body.Close()
		lastStatus = res.StatusCode
		if res.StatusCode == http.StatusOK && readErr == nil {
			out := LogsResult{InstanceID: id}
			if len(raw) > maxLogBytes {
				raw = raw[len(raw)-maxLogBytes:]
				out.Truncated = true
			}
			out.Logs = string(raw)
			return out, nil
		}
	}
	return LogsResult{}, fmt.Errorf("instance %d logs were requested but not ready (storage answered %d); try again shortly", id, lastStatus)
}
