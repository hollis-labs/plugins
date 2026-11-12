package vastaiplugin

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// These are the only shapes this plugin returns. Each is an allow-list (ADR
// 0003 in the Cerberus repo): a field Vast.ai adds is not emitted until it is
// named here. The mapping from the API's JSON lives in client.go, in explicit
// decode structs, so a reviewer sees the whole boundary in one place.
//
// Money is US dollars. Vast.ai quotes prices per hour ($/hr).

// Account is get_account's result. The API also returns the account email and
// the SSH public key; neither is returned.
type Account struct {
	UserID     int     `json:"user_id"`
	Username   string  `json:"username,omitempty" cerb:"untrusted"`
	IsTeam     bool    `json:"is_team"`
	BalanceUSD float64 `json:"balance_usd"` // spendable: credit plus balance
}

// Offer is one rentable machine, normalized so two offers compare directly:
// VRAM in GB, and the price both per hour and per GB of VRAM-hour.
type Offer struct {
	ID               int     `json:"id"`
	GPUName          string  `json:"gpu_name"`
	NumGPUs          int     `json:"num_gpus"`
	GPURAMGB         float64 `json:"gpu_ram_gb"`    // per GPU
	TotalVRAMGB      float64 `json:"total_vram_gb"` // across all GPUs
	DPHTotal         float64 `json:"dph_total"`     // $/hr for the whole offer, storage included
	DPHPerGPU        float64 `json:"dph_per_gpu"`
	USDPerVRAMGBHour float64 `json:"usd_per_vram_gb_hour"`
	StoragePerGBMo   float64 `json:"storage_usd_per_gb_month"`
	Reliability      float64 `json:"reliability"`
	DLPerf           float64 `json:"dlperf"`
	DLPerfPerDPH     float64 `json:"dlperf_per_dph"`
	Geolocation      string  `json:"geolocation" cerb:"untrusted"`
	CPUCores         float64 `json:"cpu_cores_effective"`
	CPURAMGB         float64 `json:"cpu_ram_gb"`
	DiskSpaceGB      float64 `json:"disk_space_gb"`
	InetDownMbps     float64 `json:"inet_down_mbps"`
	InetUpMbps       float64 `json:"inet_up_mbps"`
	CUDAMax          float64 `json:"cuda_max_good"`
	DirectPorts      int     `json:"direct_port_count"`
	Verified         bool    `json:"verified"`
	AvailableSeconds float64 `json:"available_seconds"` // how long the host will offer it
	MinBid           float64 `json:"min_bid,omitempty"` // $/hr floor, interruptible only
	MachineID        int     `json:"machine_id"`
}

// OfferList is search_offers' result. Query echoes the filters that were sent,
// so a result is self-describing when it is filed beside a benchmark.
type OfferList struct {
	Offers []Offer        `json:"offers"`
	Count  int            `json:"count"`
	Query  map[string]any `json:"query"`
}

// Instance is the normalized view of a rented instance. The API also returns
// the Jupyter token, environment variables, the onstart script, local IPs and
// host internals; none is returned. The cost fields are estimates from the
// instance's own clock: cost_report is the billed truth.
type Instance struct {
	ID            int               `json:"id"`
	Label         string            `json:"label" cerb:"untrusted"`
	State         string            `json:"state"`         // running, stopped, starting, unknown
	ActualStatus  string            `json:"actual_status"` // loading, running, exited, ...
	IntendedState string            `json:"intended_state"`
	StatusMsg     string            `json:"status_msg,omitempty" cerb:"untrusted"`
	GPUName       string            `json:"gpu_name"`
	NumGPUs       int               `json:"num_gpus"`
	GPURAMGB      float64           `json:"gpu_ram_gb"`
	Image         string            `json:"image" cerb:"untrusted"`
	DiskGB        float64           `json:"disk_gb"`
	Geolocation   string            `json:"geolocation" cerb:"untrusted"`
	MachineID     int               `json:"machine_id"`
	IsBid         bool              `json:"is_bid"`
	SSHHost       string            `json:"ssh_host,omitempty"`
	SSHPort       int               `json:"ssh_port,omitempty"`
	PublicIP      string            `json:"public_ip,omitempty"`
	Ports         map[string]string `json:"ports,omitempty"` // container port -> public host port
	DPHTotal      float64           `json:"dph_total"`       // $/hr while running, storage included
	StorageDPH    float64           `json:"storage_dph"`     // $/hr for the disk alone, which is what a stopped instance costs
	BurnPerHour   float64           `json:"burn_usd_per_hour"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	RuntimeHours  float64           `json:"runtime_hours"`
	EstimatedCost float64           `json:"estimated_cost_usd"` // dph_total x runtime_hours; ignores any stopped periods
}

// InstanceList is list_instances' result. Truncated says the page cap cut the
// list short, so a partial inventory never looks complete.
type InstanceList struct {
	Instances []Instance `json:"instances"`
	Count     int        `json:"count"`
	Truncated bool       `json:"truncated"`
	// BurnPerHour is what the listed instances cost per hour right now.
	BurnPerHour float64 `json:"burn_usd_per_hour"`
}

// CreatedReference is what create_instance returns when the instance was
// rented but reading it back failed. The rental succeeded and is billable, so
// it is reported as a success carrying the id rather than as an error that
// would invite a retry and a second rental.
type CreatedReference struct {
	InstanceID int    `json:"instance_id"`
	Label      string `json:"label" cerb:"untrusted"`
}

// InstanceAction reports a completed start, stop, relabel or destroy. The cost
// fields come from reading the instance just before the action, so a destroy
// leaves a final figure behind: once destroyed the instance is gone from the
// API, and only cost_report's charges remain.
type InstanceAction struct {
	InstanceID    int     `json:"instance_id"`
	Action        string  `json:"action"` // start, stop, set_label, destroy
	Label         string  `json:"label,omitempty" cerb:"untrusted"`
	GPUName       string  `json:"gpu_name,omitempty"`
	RuntimeHours  float64 `json:"runtime_hours,omitempty"`
	EstimatedCost float64 `json:"estimated_cost_usd,omitempty"`
	BurnPerHour   float64 `json:"burn_usd_per_hour,omitempty"` // the rate after the action
}

// LogsResult is get_logs' result. Container logs are text the plugin did not
// compose and can carry secrets, which is why the operation is read_sensitive.
type LogsResult struct {
	InstanceID int    `json:"instance_id"`
	Logs       string `json:"logs" cerb:"untrusted"`
	Truncated  bool   `json:"truncated"`
}

// CostReport is cost_report's result: what was billed over a window, by
// workload (the instance label) and by instance, beside what the live
// instances are costing right now.
type CostReport struct {
	From         time.Time      `json:"from"`
	To           time.Time      `json:"to"`
	TotalUSD     float64        `json:"total_usd"`
	GPUUSD       float64        `json:"gpu_usd"`
	DiskUSD      float64        `json:"disk_usd"`
	BandwidthUSD float64        `json:"bandwidth_usd"`
	ByWorkload   []WorkloadCost `json:"by_workload"`
	Instances    []InstanceCost `json:"instances"`
	// Live state, from the instances that exist now.
	BurnPerHour float64  `json:"burn_usd_per_hour"`
	BalanceUSD  *float64 `json:"balance_usd,omitempty"`
	RunwayHours *float64 `json:"runway_hours,omitempty"`
	Truncated   bool     `json:"truncated"`
	Notes       []string `json:"notes,omitempty"`
}

// WorkloadCost totals every instance that carried one label. An instance with
// no label is reported under UnlabeledWorkload, which is the signal that
// create_instance was bypassed.
type WorkloadCost struct {
	Workload     string  `json:"workload" cerb:"untrusted"`
	Instances    int     `json:"instances"`
	TotalUSD     float64 `json:"total_usd"`
	GPUUSD       float64 `json:"gpu_usd"`
	DiskUSD      float64 `json:"disk_usd"`
	BandwidthUSD float64 `json:"bandwidth_usd"`
	LiveBurn     float64 `json:"live_burn_usd_per_hour"`
}

// InstanceCost is one instance's billed charges in the window.
type InstanceCost struct {
	InstanceID   int       `json:"instance_id"`
	Workload     string    `json:"workload" cerb:"untrusted"`
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	TotalUSD     float64   `json:"total_usd"`
	GPUUSD       float64   `json:"gpu_usd"`
	DiskUSD      float64   `json:"disk_usd"`
	BandwidthUSD float64   `json:"bandwidth_usd"`
	Live         bool      `json:"live"`
}

// UnlabeledWorkload names the bucket for instances that carry no label.
const UnlabeledWorkload = "(unlabeled)"

// DryRunPreview is the preview a dry run returns instead of calling Vast.ai.
// It has the same shape as the host's own previews, so the CLI and API render
// a plugin preview like any other. The host cannot verify it: it is this
// plugin's claim of what the call would do.
type DryRunPreview struct {
	DryRun    bool           `json:"dry_run"`
	Connector string         `json:"connector"`
	Operation string         `json:"operation"`
	Summary   string         `json:"summary"`
	Target    map[string]any `json:"target,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

func newPreview(operation, summary string, target, input map[string]any) DryRunPreview {
	return DryRunPreview{
		DryRun:    true,
		Connector: ConnectorID,
		Operation: operation,
		Summary:   summary,
		Target:    target,
		Input:     input,
	}
}

// scriptDigest describes an onstart script without carrying it. A script
// routinely holds credentials, and a preview lands in agent context and logs,
// so it shows only enough to confirm which script would run: its size and
// hash. Compare with `shasum -a 256 <file>`.
func scriptDigest(script string) map[string]any {
	if script == "" {
		return map[string]any{"bytes": 0}
	}
	sum := sha256.Sum256([]byte(script))
	return map[string]any{
		"bytes":  len(script),
		"sha256": hex.EncodeToString(sum[:]),
	}
}

// envNames lists environment variable names without their values: values are
// where tokens live (HF_TOKEN and the like), and a preview is logged.
func envNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
