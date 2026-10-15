package sysmonplugin

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// collectGPUs never fails: a missing tool or a tool that errors contributes no
// devices. The result is always a non-nil slice.
func collectGPUs(ctx context.Context, e Env) []GPU {
	gpus := []GPU{}
	if e.GOOS == "darwin" {
		if out, err := e.Run(ctx, "system_profiler", "SPDisplaysDataType", "-json"); err == nil {
			gpus = append(gpus, parseAppleGPUs(out)...)
		}
		return gpus
	}
	if out, err := e.Run(ctx, "nvidia-smi",
		"--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits"); err == nil {
		gpus = append(gpus, parseNvidiaGPUs(out)...)
	}
	if out, err := e.Run(ctx, "rocm-smi", "--showproductname", "--showmeminfo", "vram", "--json"); err == nil {
		gpus = append(gpus, parseRocmGPUs(out)...)
	}
	return gpus
}

// parseNvidiaGPUs reads `name, memory.total (MiB), driver_version` CSV rows.
func parseNvidiaGPUs(out []byte) []GPU {
	gpus := []GPU{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 3 {
			continue
		}
		g := GPU{Vendor: "nvidia", Model: strings.TrimSpace(parts[0]), Driver: strings.TrimSpace(parts[len(parts)-1])}
		if mib, err := strconv.ParseUint(strings.TrimSpace(parts[len(parts)-2]), 10, 64); err == nil {
			b := mib << 20
			g.MemoryTotalBytes = &b
		}
		if g.Model != "" {
			gpus = append(gpus, g)
		}
	}
	return gpus
}

// parseRocmGPUs reads rocm-smi's JSON, keyed "card0", "card1", ...
// Some releases also emit a "system" key, which is ignored.
func parseRocmGPUs(out []byte) []GPU {
	gpus := []GPU{}
	var cards map[string]map[string]any
	if err := json.Unmarshal(out, &cards); err != nil {
		return gpus
	}
	names := make([]string, 0, len(cards))
	for k := range cards {
		if strings.HasPrefix(k, "card") {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		c := cards[k]
		model := firstString(c, "Card Series", "Card series", "Card Model", "Card model")
		if model == "" {
			continue
		}
		g := GPU{Vendor: "amd", Model: model}
		if s := firstString(c, "VRAM Total Memory (B)"); s != "" {
			if b, err := strconv.ParseUint(s, 10, 64); err == nil {
				g.MemoryTotalBytes = &b
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// parseAppleGPUs reads `system_profiler SPDisplaysDataType -json`.
func parseAppleGPUs(out []byte) []GPU {
	gpus := []GPU{}
	var doc struct {
		Displays []map[string]any `json:"SPDisplaysDataType"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return gpus
	}
	for _, d := range doc.Displays {
		model := firstString(d, "sppci_model", "_name")
		if model == "" {
			continue
		}
		g := GPU{Vendor: appleVendor(firstString(d, "spdisplays_vendor"), model), Model: model}
		if n, err := strconv.Atoi(firstString(d, "sppci_cores")); err == nil {
			g.Cores = &n
		}
		// Discrete cards report "spdisplays_vram" such as "8 GB"; unified
		// memory reports nothing and stays nil.
		if v := firstString(d, "spdisplays_vram", "spdisplays_vram_shared"); v != "" {
			if b, ok := parseSizeString(v); ok {
				g.MemoryTotalBytes = &b
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func appleVendor(raw, model string) string {
	l := strings.ToLower(raw + " " + model)
	switch {
	case strings.Contains(l, "apple"):
		return "apple"
	case strings.Contains(l, "amd") || strings.Contains(l, "radeon"):
		return "amd"
	case strings.Contains(l, "nvidia"):
		return "nvidia"
	case strings.Contains(l, "intel"):
		return "intel"
	}
	return "unknown"
}

// parseSizeString reads "8 GB", "1536 MB".
func parseSizeString(s string) (uint64, bool) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return 0, false
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToUpper(f[1]) {
	case "MB":
		return n << 20, true
	case "GB":
		return n << 30, true
	}
	return 0, false
}
