package sysmonplugin

// The DTOs below are this plugin's own allow-list shapes (ADR 0003). Every
// field is mapped explicitly from parsed host output; nothing is a vendor
// struct and no raw command output is passed through. Text that originates
// on the host (model strings, hostnames, mount points) is labelled untrusted.

// LoadAverage is the 1, 5 and 15 minute load averages.
type LoadAverage struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// CPU describes the processor. UsagePercent is nil when the platform path
// cannot sample it (macOS today).
type CPU struct {
	Model        string      `json:"model" cerb:"untrusted"`
	Cores        int         `json:"cores"`
	UsagePercent *float64    `json:"usage_percent,omitempty"`
	Load         LoadAverage `json:"load"`
}

// Memory is reported in bytes. AvailableBytes is memory usable without
// swapping (MemAvailable on Linux; free+inactive+speculative+purgeable on
// macOS).
type Memory struct {
	TotalBytes     uint64 `json:"total_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

// Mount is one mounted filesystem.
type Mount struct {
	Mount      string  `json:"mount" cerb:"untrusted"`
	Device     string  `json:"device" cerb:"untrusted"`
	FSType     string  `json:"fs_type,omitempty" cerb:"untrusted"`
	SizeBytes  uint64  `json:"size_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	AvailBytes uint64  `json:"avail_bytes"`
	UsedPct    float64 `json:"used_percent"`
}

// Disk is the disk operation result.
type Disk struct {
	Mounts []Mount `json:"mounts"`
}

// GPU is one graphics device. MemoryTotalBytes is nil when the device has no
// dedicated memory figure (Apple unified memory).
type GPU struct {
	Vendor           string  `json:"vendor"`
	Model            string  `json:"model" cerb:"untrusted"`
	MemoryTotalBytes *uint64 `json:"memory_total_bytes,omitempty"`
	Cores            *int    `json:"cores,omitempty"`
	Driver           string  `json:"driver,omitempty" cerb:"untrusted"`
}

// Host is the operating system identity and uptime.
type Host struct {
	Hostname      string `json:"hostname" cerb:"untrusted"`
	OS            string `json:"os"`
	OSVersion     string `json:"os_version,omitempty" cerb:"untrusted"`
	Arch          string `json:"arch"`
	Kernel        string `json:"kernel" cerb:"untrusted"`
	UptimeSeconds uint64 `json:"uptime_seconds"`
}

// HostReport aggregates every section. A section that could not be collected
// is zero/empty and named in Warnings (fixed text, never command output).
// GPUs is always a list, empty when no supported tool is present.
type HostReport struct {
	Host     Host     `json:"host"`
	CPU      CPU      `json:"cpu"`
	Memory   Memory   `json:"memory"`
	Disks    []Mount  `json:"disks"`
	GPUs     []GPU    `json:"gpus"`
	Warnings []string `json:"warnings"`
}
