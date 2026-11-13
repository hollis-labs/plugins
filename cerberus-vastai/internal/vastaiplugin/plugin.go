package vastaiplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/libs/mcp/plugin-mcp/plugin-sdk/subprocess"
)

// errMissingCredential is what every Vast.ai call returns when no key arrived.
// It is short on purpose: the host recognises a failure from a plugin that
// loaded without a required secret and appends its own guidance naming every
// way to supply one, so repeating it here would print it twice.
var errMissingCredential = errors.New("no Vast.ai API key was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes, no
// flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply it as " + KeyEnvVar +
	", as the api_key entry under vastai in connector-secrets.yaml, or as keychain://vastai/api_key, " +
	"then reload the plugin with `cerberus connectors plugin managed load vastai`"

// Plugin serves the Vast.ai connector over the plugin-sdk subprocess protocol.
type Plugin struct {
	newBackend func(apiKey string) Backend
	config     subprocess.ConfigReader
	backend    Backend
	scrub      scrubber
	now        func() time.Time
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live Vast.ai API. The key is not looked up
// here: Cerberus resolves every secret this manifest declares and hands the
// values over in init config.
func New() *Plugin { return &Plugin{newBackend: NewHTTPBackend, now: time.Now} }

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(string) Backend { return backend },
		backend:    backend,
		now:        time.Now,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:                 def.ID,
		Name:               "Cerberus Vast.ai Connector",
		Version:            def.Version,
		Description:        "Vast.ai GPU rental for Cerberus: offers, instances and cost per workload",
		Protocol:           subprocess.ProtocolVersion,
		CapabilityContract: 1,
	}, nil
}

// Load never fails on a missing key. The plugin starts, reports the gap in
// Health, and each Vast.ai call fails with errMissingCredential. Dry runs need
// no credential and keep working.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	key := p.key()
	p.scrub = newScrubber(key)
	if p.backend == nil && key != "" {
		p.backend = p.newBackend(key)
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// Health makes no network call: it reports whether the plugin can reach
// Vast.ai at all, which is whether a credential arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: credentialGuidance}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "API key configured"}, nil
}

// key reads the credential the host resolved, falling back to the environment
// only for a binary run directly, outside the host, which receives no init
// config.
func (p *Plugin) key() string {
	if p.config != nil {
		if key := p.config.Secret(SecretAPIKey); key != "" {
			return key
		}
	}
	return os.Getenv(KeyEnvVar)
}

// ackOperations are the operations that change Vast.ai, and so need
// acknowledgment; previewOperations are the ones among them with a dry-run
// preview. The contract says the same thing, and TestGatesMatchTheContract
// holds the two in step.
var (
	ackOperations = map[string]bool{
		"create_instance": true,
		"start":           true,
		"stop":            true,
		"set_label":       true,
		"destroy":         true,
	}
	previewOperations = map[string]bool{
		"create_instance": true,
		"stop":            true,
		"destroy":         true,
	}
)

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		return withOperationTelemetry(req, p.scrubResult(result)), nil
	}
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, p.scrub.text(err.Error())), nil
	}
	return result, p.scrub.err(err)
}

// scrubResult removes the key from a successful result as well. Only logs can
// carry one, but the cost of checking every result is a string replace.
func (p *Plugin) scrubResult(result subprocess.MCPCallResult) subprocess.MCPCallResult {
	if len(p.scrub.values) == 0 || len(result.Content) == 0 {
		return result
	}
	result.Content = []byte(p.scrub.text(string(result.Content)))
	return result
}

// errorCode is the Cerberus code for a failed call. A missing key and a key
// Vast.ai rejects (401, or the 403 and 404 auth_error it answers with) are
// credential_missing; an API that cannot be reached at all is unavailable;
// arguments this plugin refused, and an instance id Vast.ai does not know
// (404), are invalid_args. Anything else the API answered stays uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *APIError
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr):
		switch {
		case apiErr.Status == http.StatusUnauthorized, apiErr.Code == "auth_error":
			return cerbplugin.ErrorCredentialMissing
		case apiErr.Status == http.StatusNotFound:
			return cerbplugin.ErrorInvalidArgs
		}
		return ""
	case isUnreachable(err):
		return cerbplugin.ErrorUnavailable
	}
	return ""
}

// isUnreachable matches a network-level failure: a refused connection, a name
// that does not resolve, a timeout. An HTTP status is not one of these.
func isUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// Search bounds.
const (
	defaultOfferLimit = 20
	maxOfferLimit     = 100
	defaultLogTail    = 200
	maxLogTail        = 2000
	defaultDiskGB     = 20
	defaultCostDays   = 7
	maxCostDays       = 90
)

var offerTypes = map[string]string{"ondemand": "ondemand", "on-demand": "ondemand", "bid": "bid", "reserved": "reserved"}

// orderColumns are the sort columns search_offers accepts, and whether bigger
// is better (so the default direction is descending).
var orderColumns = map[string]bool{
	"dph_total":           false,
	"dlperf_per_dphtotal": true,
	"dlperf":              true,
	"reliability":         true,
	"gpu_ram":             true,
	"num_gpus":            true,
}

func (p *Plugin) call(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	args := newArgMap(req.Arguments)

	dryRun := false
	if args.boolean(argDryRun) && !previewOperations[op.Name] {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s has no dry-run preview; run it without --dry-run", op.Name))
	}
	if ackOperations[op.Name] {
		var err error
		if dryRun, err = p.writeMode(args, op.Name); err != nil {
			return subprocess.MCPCallResult{}, err
		}
	}

	switch op.Name {
	case "get_account":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetAccount(ctx))

	case "search_offers":
		return p.searchOffers(ctx, args)

	case "list_instances":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListInstances(ctx))

	case "get_instance":
		id := args.instanceID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetInstance(ctx, id))

	case "status":
		id := args.instanceID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		instance, err := p.backend.GetInstance(ctx, id)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(string(instanceResourceState(instance.State)), nil)

	case "cost_report":
		return p.costReport(ctx, args)

	case "get_logs":
		return p.getLogs(ctx, args)

	case "create_instance":
		return p.createInstance(ctx, args, dryRun)

	case "start":
		id := args.instanceID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		before := p.snapshot(ctx, id)
		if err := p.backend.SetState(ctx, id, "running"); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		action := actionFrom(id, "start", before)
		if before != nil {
			action.BurnPerHour = before.DPHTotal
		}
		return marshalResult(action, nil)

	case "stop":
		return p.stopOrDestroy(ctx, args, dryRun, "stop")

	case "destroy":
		return p.stopOrDestroy(ctx, args, dryRun, "destroy")

	case "set_label":
		id := args.instanceID()
		label := args.required("label")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.backend.SetLabel(ctx, id, label); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(InstanceAction{InstanceID: id, Action: "set_label", Label: label}, nil)

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

// --- search ----------------------------------------------------------------

func (p *Plugin) searchOffers(ctx context.Context, args *argMap) (subprocess.MCPCallResult, error) {
	q := OfferQuery{
		NumGPUs:        derefInt(args.intPtr("num_gpus")),
		MinGPUs:        derefInt(args.intPtr("min_gpus")),
		MinVRAMGB:      derefFloat(args.floatPtr("min_vram_gb")),
		MinTotalVRAMGB: derefFloat(args.floatPtr("min_total_vram_gb")),
		MaxDPH:         derefFloat(args.floatPtr("max_price")),
		MinReliability: derefFloat(args.floatPtr("min_reliability")),
		MinDiskGB:      derefFloat(args.floatPtr("min_disk_gb")),
		MinInetDown:    derefFloat(args.floatPtr("min_inet_down")),
		Geolocation:    upperAll(args.strSlice("geolocation")),
		Unverified:     args.boolean("include_unverified"),
		Limit:          defaultOfferLimit,
	}
	for _, name := range args.strSlice("gpu_name") {
		// The API lists GPUs by their display name, "RTX 4090"; the CLI's
		// shorthand is RTX_4090. Accept either.
		q.GPUNames = append(q.GPUNames, strings.ReplaceAll(name, "_", " "))
	}
	if t := strings.ToLower(args.str("type")); t != "" {
		normal, ok := offerTypes[t]
		if !ok {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("type must be ondemand, bid or reserved, got %q", t))
		}
		q.Type = normal
	}
	if limit := args.intPtr("limit"); limit != nil {
		if *limit == 0 || *limit > maxOfferLimit {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("limit must be between 1 and %d", maxOfferLimit))
		}
		q.Limit = *limit
	}
	if order := args.str("order_by"); order != "" {
		bigger, ok := orderColumns[order]
		if !ok {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("order_by must be one of %s, got %q", strings.Join(sortedKeys(orderColumns), ", "), order))
		}
		q.OrderBy, q.Descending = order, bigger
	}
	if args.has("descending") {
		q.Descending = args.boolean("descending")
	}
	if err := invalid(args.err()); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if err := p.ready(); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	offers, err := p.backend.SearchOffers(ctx, q)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return marshalResult(OfferList{Offers: offers, Count: len(offers), Query: queryEcho(q)}, nil)
}

// queryEcho is the filters as sent, with zero values dropped.
func queryEcho(q OfferQuery) map[string]any {
	out := map[string]any{}
	set := func(k string, v any, on bool) {
		if on {
			out[k] = v
		}
	}
	set("gpu_name", q.GPUNames, len(q.GPUNames) > 0)
	set("num_gpus", q.NumGPUs, q.NumGPUs > 0)
	set("min_gpus", q.MinGPUs, q.MinGPUs > 0)
	set("min_vram_gb", q.MinVRAMGB, q.MinVRAMGB > 0)
	set("min_total_vram_gb", q.MinTotalVRAMGB, q.MinTotalVRAMGB > 0)
	set("max_price", q.MaxDPH, q.MaxDPH > 0)
	set("min_reliability", q.MinReliability, q.MinReliability > 0)
	set("min_disk_gb", q.MinDiskGB, q.MinDiskGB > 0)
	set("min_inet_down", q.MinInetDown, q.MinInetDown > 0)
	set("geolocation", q.Geolocation, len(q.Geolocation) > 0)
	set("type", orDefault(q.Type, "ondemand"), true)
	set("include_unverified", true, q.Unverified)
	set("order_by", orDefault(q.OrderBy, "dph_total"), true)
	set("descending", q.Descending, true)
	set("limit", q.Limit, true)
	return out
}

// --- create ----------------------------------------------------------------

func (p *Plugin) createInstance(ctx context.Context, args *argMap, dryRun bool) (subprocess.MCPCallResult, error) {
	req := CreateRequest{
		OfferID: args.positiveID("offer_id"),
		Image:   args.required("image"),
		Label:   args.required("label"),
		DiskGB:  defaultDiskGB,
		Onstart: args.rawString("onstart"),
		Env:     args.stringMap("env"),
		Runtype: args.str("runtype"),
	}
	if disk := args.floatPtr("disk_gb"); disk != nil {
		if *disk == 0 {
			args.problems = append(args.problems, "disk_gb must be greater than zero")
		} else {
			req.DiskGB = *disk
		}
	}
	maxPrice := args.floatPtr("max_price")
	if args.has("start") && !args.boolean("start") {
		req.TargetState = "stopped"
	}
	if strings.ContainsAny(req.Image, " \t\r\n") {
		args.problems = append(args.problems, "image must not contain whitespace")
	}
	if err := invalid(args.err()); err != nil {
		return subprocess.MCPCallResult{}, err
	}

	if dryRun {
		return marshalResult(p.createPreview(ctx, req, maxPrice), nil)
	}
	if err := p.ready(); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	// A price ceiling is only worth anything if it is checked against the
	// price the offer has now, so with one set an offer that cannot be read
	// is refused rather than rented unchecked.
	if maxPrice != nil {
		offer, err := p.backend.GetOffer(ctx, req.OfferID)
		if err != nil {
			return subprocess.MCPCallResult{}, fmt.Errorf("max_price was set but offer %d could not be priced, so nothing was rented: %w", req.OfferID, err)
		}
		if offer == nil {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("offer %d is no longer available; search again", req.OfferID))
		}
		if offer.DPHTotal > *maxPrice {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("offer %d costs $%.4f/hour, above max_price $%.4f; nothing was rented", req.OfferID, offer.DPHTotal, *maxPrice))
		}
	}
	id, err := p.backend.CreateInstance(ctx, req)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	instance, err := p.backend.GetInstance(ctx, id)
	if err != nil {
		// Rented, billable, and not to be retried: report the id.
		return marshalResult(CreatedReference{InstanceID: id, Label: req.Label}, nil)
	}
	return marshalResult(instance, nil)
}

// createPreview describes a rental without making it. It prices the offer when
// a credential is available, and says so when it could not.
func (p *Plugin) createPreview(ctx context.Context, req CreateRequest, maxPrice *float64) DryRunPreview {
	target := map[string]any{"offer_id": req.OfferID}
	input := map[string]any{
		"image":   req.Image,
		"label":   req.Label,
		"disk_gb": req.DiskGB,
		// Values are where tokens live, so only the names are shown.
		"env_names": envNames(req.Env),
		"onstart":   scriptDigest(req.Onstart),
		"start":     req.TargetState != "stopped",
	}
	if req.Runtype != "" {
		input["runtype"] = req.Runtype
	}
	if maxPrice != nil {
		input["max_price"] = *maxPrice
	}
	preview := newPreview("create_instance", "Would rent a Vast.ai offer and start an instance on it. Billing starts at once.", target, input)

	if p.backend == nil {
		preview.Warnings = append(preview.Warnings, "No API key is configured, so the offer was not checked: its price and availability are unknown.")
		return preview
	}
	offer, err := p.backend.GetOffer(ctx, req.OfferID)
	switch {
	case err != nil:
		preview.Warnings = append(preview.Warnings, "The offer could not be checked: "+p.scrub.text(err.Error()))
	case offer == nil:
		preview.Warnings = append(preview.Warnings, fmt.Sprintf("Offer %d was not found. It may have been rented or withdrawn; search again.", req.OfferID))
	default:
		target["gpu"] = fmt.Sprintf("%dx %s (%.0f GB each)", offer.NumGPUs, offer.GPUName, offer.GPURAMGB)
		target["usd_per_hour"] = offer.DPHTotal
		target["usd_per_day"] = round(offer.DPHTotal*24, 2)
		target["reliability"] = offer.Reliability
		target["geolocation"] = offer.Geolocation
		if maxPrice != nil && offer.DPHTotal > *maxPrice {
			preview.Warnings = append(preview.Warnings, fmt.Sprintf("The offer costs $%.4f/hour, above max_price $%.4f. The real run would refuse.", offer.DPHTotal, *maxPrice))
		}
		if req.DiskGB > offer.DiskSpaceGB && offer.DiskSpaceGB > 0 {
			preview.Warnings = append(preview.Warnings, fmt.Sprintf("disk_gb %.0f is more than the %.0f GB this host offers.", req.DiskGB, offer.DiskSpaceGB))
		}
	}
	if maxPrice == nil {
		preview.Warnings = append(preview.Warnings, "No max_price is set, so the price is not re-checked when the real run executes.")
	}
	return preview
}

// --- stop and destroy ------------------------------------------------------

func (p *Plugin) stopOrDestroy(ctx context.Context, args *argMap, dryRun bool, which string) (subprocess.MCPCallResult, error) {
	id := args.instanceID()
	if err := invalid(args.err()); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	summary := "Would stop a Vast.ai instance. GPU billing ends; the disk keeps billing, and the GPU may be taken by another renter."
	if which == "destroy" {
		summary = "Would destroy a Vast.ai instance and its disk permanently. All billing stops."
	}
	if dryRun {
		preview := newPreview(which, summary, map[string]any{"instance_id": id}, nil)
		if p.backend != nil {
			if before, err := p.backend.GetInstance(ctx, id); err == nil {
				preview.Target["label"] = before.Label
				preview.Target["gpu"] = fmt.Sprintf("%dx %s", before.NumGPUs, before.GPUName)
				preview.Target["state"] = before.State
				preview.Target["runtime_hours"] = before.RuntimeHours
				preview.Target["estimated_cost_usd"] = before.EstimatedCost
			} else {
				preview.Warnings = append(preview.Warnings, "The instance could not be read: "+p.scrub.text(err.Error()))
			}
		}
		return marshalResult(preview, nil)
	}
	if err := p.ready(); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	// Read first: a destroyed instance is gone from the API, so this is the
	// last chance to record what it cost. A failed read does not block the
	// action, except that destroying an instance that does not exist is
	// reported as such.
	before := p.snapshot(ctx, id)
	if which == "stop" {
		if err := p.backend.SetState(ctx, id, "stopped"); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		action := actionFrom(id, "stop", before)
		if before != nil {
			action.BurnPerHour = before.StorageDPH
		}
		return marshalResult(action, nil)
	}
	if err := p.backend.Destroy(ctx, id); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return marshalResult(actionFrom(id, "destroy", before), nil)
}

// snapshot reads an instance for the action's cost figures, or nil if it
// cannot be read.
func (p *Plugin) snapshot(ctx context.Context, id int) *Instance {
	instance, err := p.backend.GetInstance(ctx, id)
	if err != nil {
		return nil
	}
	return instance
}

func actionFrom(id int, action string, before *Instance) InstanceAction {
	out := InstanceAction{InstanceID: id, Action: action}
	if before != nil {
		out.Label = before.Label
		out.GPUName = before.GPUName
		out.RuntimeHours = before.RuntimeHours
		out.EstimatedCost = before.EstimatedCost
	}
	return out
}

// --- logs ------------------------------------------------------------------

func (p *Plugin) getLogs(ctx context.Context, args *argMap) (subprocess.MCPCallResult, error) {
	id := args.instanceID()
	req := LogsRequest{Tail: defaultLogTail, Filter: args.str("filter"), Daemon: args.boolean("daemon")}
	if tail := args.intPtr("tail"); tail != nil {
		if *tail == 0 || *tail > maxLogTail {
			args.problems = append(args.problems, fmt.Sprintf("tail must be between 1 and %d", maxLogTail))
		} else {
			req.Tail = *tail
		}
	}
	if err := invalid(args.err()); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if err := p.ready(); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return marshalResult(p.backend.Logs(ctx, id, req))
}

// --- cost ------------------------------------------------------------------

func (p *Plugin) costReport(ctx context.Context, args *argMap) (subprocess.MCPCallResult, error) {
	days := defaultCostDays
	if d := args.intPtr("days"); d != nil {
		if *d == 0 || *d > maxCostDays {
			args.problems = append(args.problems, fmt.Sprintf("days must be between 1 and %d", maxCostDays))
		} else {
			days = *d
		}
	}
	only := args.str("workload")
	if err := invalid(args.err()); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	if err := p.ready(); err != nil {
		return subprocess.MCPCallResult{}, err
	}
	now := p.clock().UTC()
	// The charges API windows by whole UTC days.
	from := now.AddDate(0, 0, -days).Truncate(24 * time.Hour)
	charges, err := p.backend.Charges(ctx, from, now)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	live, err := p.backend.ListInstances(ctx)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	report := buildCostReport(from, now, charges, live, only)

	// Balance is context, not the answer: a failure to read it does not fail
	// the report.
	if account, err := p.backend.GetAccount(ctx); err == nil {
		balance := account.BalanceUSD
		report.BalanceUSD = &balance
		if report.BurnPerHour > 0 {
			runway := round(balance/report.BurnPerHour, 1)
			report.RunwayHours = &runway
		}
	} else {
		report.Notes = append(report.Notes, "The credit balance could not be read, so no runway is given.")
	}
	return marshalResult(report, nil)
}

// buildCostReport groups charges by instance and by label. It is pure so the
// grouping can be tested without a backend.
func buildCostReport(from, to time.Time, charges ChargeSet, live InstanceList, only string) CostReport {
	report := CostReport{From: from, To: to, Truncated: charges.Truncated || live.Truncated,
		ByWorkload: []WorkloadCost{}, Instances: []InstanceCost{}}

	liveByID := map[int]Instance{}
	for _, i := range live.Instances {
		liveByID[i.ID] = i
	}
	workloadOf := func(label string) string {
		if strings.TrimSpace(label) == "" {
			return UnlabeledWorkload
		}
		return label
	}

	byInstance := map[int]*InstanceCost{}
	for _, c := range charges.Charges {
		label := c.Label
		if i, ok := liveByID[c.InstanceID]; ok && label == "" {
			label = i.Label
		}
		workload := workloadOf(label)
		if only != "" && workload != only {
			continue
		}
		row, ok := byInstance[c.InstanceID]
		if !ok {
			row = &InstanceCost{InstanceID: c.InstanceID, Workload: workload, Start: c.Start, End: c.End}
			byInstance[c.InstanceID] = row
		}
		if c.Start.Before(row.Start) {
			row.Start = c.Start
		}
		if c.End.After(row.End) {
			row.End = c.End
		}
		row.TotalUSD += c.Total
		row.GPUUSD += c.GPU
		row.DiskUSD += c.Disk
		row.BandwidthUSD += c.Bandwidth
	}
	// A live instance with no charges yet still belongs in the report.
	for id, i := range liveByID {
		workload := workloadOf(i.Label)
		if only != "" && workload != only {
			continue
		}
		if _, ok := byInstance[id]; !ok {
			byInstance[id] = &InstanceCost{InstanceID: id, Workload: workload}
		}
	}

	workloads := map[string]*WorkloadCost{}
	for id, row := range byInstance {
		_, row.Live = liveByID[id]
		row.TotalUSD, row.GPUUSD, row.DiskUSD, row.BandwidthUSD =
			round(row.TotalUSD, 3), round(row.GPUUSD, 3), round(row.DiskUSD, 3), round(row.BandwidthUSD, 3)
		report.Instances = append(report.Instances, *row)

		w, ok := workloads[row.Workload]
		if !ok {
			w = &WorkloadCost{Workload: row.Workload}
			workloads[row.Workload] = w
		}
		w.Instances++
		w.TotalUSD += row.TotalUSD
		w.GPUUSD += row.GPUUSD
		w.DiskUSD += row.DiskUSD
		w.BandwidthUSD += row.BandwidthUSD
		if row.Live {
			w.LiveBurn += liveByID[id].BurnPerHour
			report.BurnPerHour += liveByID[id].BurnPerHour
		}
		report.TotalUSD += row.TotalUSD
		report.GPUUSD += row.GPUUSD
		report.DiskUSD += row.DiskUSD
		report.BandwidthUSD += row.BandwidthUSD
	}
	for _, w := range workloads {
		w.TotalUSD, w.GPUUSD, w.DiskUSD, w.BandwidthUSD, w.LiveBurn =
			round(w.TotalUSD, 3), round(w.GPUUSD, 3), round(w.DiskUSD, 3), round(w.BandwidthUSD, 3), round(w.LiveBurn, 4)
		report.ByWorkload = append(report.ByWorkload, *w)
	}
	sort.Slice(report.ByWorkload, func(i, j int) bool {
		if report.ByWorkload[i].TotalUSD != report.ByWorkload[j].TotalUSD {
			return report.ByWorkload[i].TotalUSD > report.ByWorkload[j].TotalUSD
		}
		return report.ByWorkload[i].Workload < report.ByWorkload[j].Workload
	})
	sort.Slice(report.Instances, func(i, j int) bool { return report.Instances[i].InstanceID < report.Instances[j].InstanceID })
	report.TotalUSD, report.GPUUSD, report.DiskUSD, report.BandwidthUSD, report.BurnPerHour =
		round(report.TotalUSD, 3), round(report.GPUUSD, 3), round(report.DiskUSD, 3), round(report.BandwidthUSD, 3), round(report.BurnPerHour, 4)

	report.Notes = append(report.Notes,
		"Totals are what Vast.ai has billed in the window; a running instance's latest usage may not be in them yet.",
		"burn_usd_per_hour is what the live instances cost right now: the full rate if running, the disk alone if stopped.")
	if charges.Truncated {
		report.Notes = append(report.Notes, "The charge list hit the page cap, so older charges in the window are missing.")
	}
	if w := byWorkload(report.ByWorkload, UnlabeledWorkload); w != nil {
		report.Notes = append(report.Notes, fmt.Sprintf("%d instance(s) carry no label, so their cost cannot be attributed to a workload. Use set_label.", w.Instances))
	}
	return report
}

func byWorkload(list []WorkloadCost, name string) *WorkloadCost {
	for i := range list {
		if list[i].Workload == name {
			return &list[i]
		}
	}
	return nil
}

// --- shared ----------------------------------------------------------------

// clock is the plugin's notion of now; a zero Plugin uses the real one.
func (p *Plugin) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses an acknowledgment-gated operation without --ack
// before the call reaches this process, so under the host this check never
// fires. It is here for the binary driven any other way, so that the plugin's
// own contract is "no unacknowledged write" rather than "no unacknowledged
// write provided the caller checked". A dry run changes nothing and needs no
// acknowledgment.
func (p *Plugin) writeMode(args *argMap, operation string) (bool, error) {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		return false, fmt.Errorf("%s changes Vast.ai and requires acknowledgment. Re-run it acknowledged (--ack)%s", operation, previewHint(operation))
	}
	return dryRun, nil
}

func previewHint(operation string) string {
	if previewOperations[operation] {
		return ", or preview it first with --dry-run"
	}
	return ""
}

// invalid codes this plugin's own refusal of the caller's arguments.
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
}

func (p *Plugin) ready() error {
	if p.backend == nil {
		return errMissingCredential
	}
	return nil
}

func marshalResult[T any](data T, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func upperAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.ToUpper(s))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// instanceResourceState maps an instance State string onto the Cerberus
// resource state the host speaks.
func instanceResourceState(state string) resource.State {
	switch state {
	case "running":
		return resource.StateRunning
	case "starting":
		return resource.StateStarting
	case "stopped":
		return resource.StateStopped
	case "failed":
		return resource.StateFailed
	}
	return resource.StateUnknown
}
