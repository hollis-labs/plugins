package sysmonplugin

import (
	"fmt"
	"strconv"
	"strings"
)

// parseCPUInfo returns the logical core count and a model string from
// /proc/cpuinfo. ARM kernels have no "model name"; "Model" or "Hardware"
// stand in.
func parseCPUInfo(data []byte) (cores int, model string) {
	var alt string
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			cores++
		case "model name":
			if model == "" {
				model = v
			}
		case "Model", "Hardware":
			if alt == "" {
				alt = v
			}
		}
	}
	if model == "" {
		model = alt
	}
	return cores, model
}

// parseMeminfo maps /proc/meminfo (kB values) into Memory.
func parseMeminfo(data []byte) (Memory, error) {
	kb := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		if n, err := strconv.ParseUint(f[0], 10, 64); err == nil {
			kb[strings.TrimSpace(k)] = n * 1024
		}
	}
	total, ok := kb["MemTotal"]
	if !ok {
		return Memory{}, fmt.Errorf("MemTotal missing from meminfo")
	}
	avail, ok := kb["MemAvailable"]
	if !ok { // kernels before 3.14
		avail = kb["MemFree"] + kb["Buffers"] + kb["Cached"]
	}
	m := Memory{
		TotalBytes: total, FreeBytes: kb["MemFree"], AvailableBytes: avail,
		SwapTotalBytes: kb["SwapTotal"], SwapFreeBytes: kb["SwapFree"],
	}
	if total > avail {
		m.UsedBytes = total - avail
	}
	return m, nil
}

// parseLoadavg reads /proc/loadavg.
func parseLoadavg(data []byte) (LoadAverage, error) {
	f := strings.Fields(string(data))
	if len(f) < 3 {
		return LoadAverage{}, fmt.Errorf("unrecognised loadavg")
	}
	var v [3]float64
	for i := range v {
		x, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return LoadAverage{}, err
		}
		v[i] = x
	}
	return LoadAverage{One: v[0], Five: v[1], Fifteen: v[2]}, nil
}

// parseUptime reads /proc/uptime.
func parseUptime(data []byte) (uint64, error) {
	f := strings.Fields(string(data))
	if len(f) < 1 {
		return 0, fmt.Errorf("unrecognised uptime")
	}
	s, err := strconv.ParseFloat(f[0], 64)
	if err != nil || s < 0 {
		return 0, fmt.Errorf("unrecognised uptime")
	}
	return uint64(s), nil
}

// cpuTimes returns busy and total jiffies from the aggregate line of /proc/stat.
func cpuTimes(data []byte) (busy, total uint64, err error) {
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var idle uint64
		for i, s := range f[1:] {
			n, perr := strconv.ParseUint(s, 10, 64)
			if perr != nil {
				return 0, 0, perr
			}
			if i >= 8 { // guest times are already counted in user/nice
				break
			}
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		return total - idle, total, nil
	}
	return 0, 0, fmt.Errorf("aggregate cpu line missing")
}

func cpuUsagePercent(first, second []byte) (float64, error) {
	b1, t1, err := cpuTimes(first)
	if err != nil {
		return 0, err
	}
	b2, t2, err := cpuTimes(second)
	if err != nil {
		return 0, err
	}
	if t2 <= t1 || b2 < b1 {
		return 0, fmt.Errorf("no cpu time elapsed")
	}
	return roundTenth(float64(b2-b1) / float64(t2-t1) * 100), nil
}

func roundTenth(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// parseOSRelease returns PRETTY_NAME (or NAME VERSION_ID) from /etc/os-release.
func parseOSRelease(data []byte) string {
	vals := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			vals[k] = strings.Trim(v, `"'`)
		}
	}
	if v := vals["PRETTY_NAME"]; v != "" {
		return v
	}
	return strings.TrimSpace(vals["NAME"] + " " + vals["VERSION_ID"])
}
