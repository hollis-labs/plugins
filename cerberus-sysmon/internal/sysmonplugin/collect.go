package sysmonplugin

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func supported(e Env) error {
	if e.GOOS != "linux" && e.GOOS != "darwin" {
		return fmt.Errorf("sysmon supports linux and darwin; this host is %s", e.GOOS)
	}
	return nil
}

func sysctl(ctx context.Context, e Env, key string) (string, error) {
	out, err := e.Run(ctx, "sysctl", "-n", key)
	if err != nil {
		return "", fmt.Errorf("sysctl %s unavailable", key)
	}
	return strings.TrimSpace(string(out)), nil
}

// CollectCPU reports model, logical cores, load averages and (Linux) a short
// usage sample.
func CollectCPU(ctx context.Context, e Env) (CPU, error) {
	if err := supported(e); err != nil {
		return CPU{}, err
	}
	var c CPU
	if e.GOOS == "linux" {
		data, err := e.ReadFile("/proc/cpuinfo")
		if err != nil {
			return CPU{}, fmt.Errorf("cpu info unavailable")
		}
		c.Cores, c.Model = parseCPUInfo(data)
		if l, err := e.ReadFile("/proc/loadavg"); err == nil {
			c.Load, _ = parseLoadavg(l)
		}
		if first, err := e.ReadFile("/proc/stat"); err == nil {
			e.Sleep(200 * time.Millisecond)
			if second, err := e.ReadFile("/proc/stat"); err == nil {
				if u, err := cpuUsagePercent(first, second); err == nil {
					c.UsagePercent = &u
				}
			}
		}
		return c, nil
	}
	n, err := sysctl(ctx, e, "hw.logicalcpu")
	if err != nil {
		return CPU{}, err
	}
	c.Cores, _ = strconv.Atoi(n)
	c.Model, _ = sysctl(ctx, e, "machdep.cpu.brand_string")
	if out, err := e.Run(ctx, "sysctl", "-n", "vm.loadavg"); err == nil {
		c.Load, _ = parseSysctlLoad(out)
	}
	return c, nil
}

// CollectMemory reports memory and swap in bytes.
func CollectMemory(ctx context.Context, e Env) (Memory, error) {
	if err := supported(e); err != nil {
		return Memory{}, err
	}
	if e.GOOS == "linux" {
		data, err := e.ReadFile("/proc/meminfo")
		if err != nil {
			return Memory{}, fmt.Errorf("memory info unavailable")
		}
		return parseMeminfo(data)
	}
	s, err := sysctl(ctx, e, "hw.memsize")
	if err != nil {
		return Memory{}, err
	}
	total, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return Memory{}, fmt.Errorf("unrecognised hw.memsize")
	}
	out, err := e.Run(ctx, "vm_stat")
	if err != nil {
		return Memory{}, fmt.Errorf("vm_stat unavailable")
	}
	m, err := parseVMStat(out, total)
	if err != nil {
		return Memory{}, err
	}
	if sw, err := e.Run(ctx, "sysctl", "-n", "vm.swapusage"); err == nil {
		m.SwapTotalBytes, m.SwapFreeBytes = parseSwapUsage(sw)
	}
	return m, nil
}

// CollectHost reports identity, kernel and uptime.
func CollectHost(ctx context.Context, e Env) (Host, error) {
	if err := supported(e); err != nil {
		return Host{}, err
	}
	h := Host{OS: e.GOOS, Arch: e.GOARCH}
	h.Hostname, _ = e.Hostname()
	if e.GOOS == "linux" {
		if data, err := e.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
			h.Kernel = strings.TrimSpace(string(data))
		}
		if data, err := e.ReadFile("/etc/os-release"); err == nil {
			h.OSVersion = parseOSRelease(data)
		}
		data, err := e.ReadFile("/proc/uptime")
		if err != nil {
			return h, fmt.Errorf("uptime unavailable")
		}
		h.UptimeSeconds, err = parseUptime(data)
		return h, err
	}
	h.Kernel, _ = sysctl(ctx, e, "kern.osrelease")
	if out, err := e.Run(ctx, "sw_vers", "-productVersion"); err == nil {
		h.OSVersion = strings.TrimSpace(string(out))
	}
	s, err := sysctl(ctx, e, "kern.boottime")
	if err != nil {
		return h, err
	}
	boot, err := parseBootTime([]byte(s))
	if err != nil {
		return h, err
	}
	if d := e.Now().Unix() - boot; d > 0 {
		h.UptimeSeconds = uint64(d)
	}
	return h, nil
}

// CollectDisk reports one filesystem (the one holding path) or, when path is
// empty, every real mount.
func CollectDisk(ctx context.Context, e Env, path string) (Disk, error) {
	if err := supported(e); err != nil {
		return Disk{}, err
	}
	var (
		m   []Mount
		err error
	)
	if path == "" {
		m, err = collectDisks(ctx, e)
	} else {
		m, err = collectDiskPath(ctx, e, path)
	}
	if err != nil {
		return Disk{}, err
	}
	if m == nil {
		m = []Mount{}
	}
	return Disk{Mounts: m}, nil
}

// CollectReport gathers every section best-effort. A failed section leaves a
// fixed warning rather than failing the report.
func CollectReport(ctx context.Context, e Env) (HostReport, error) {
	if err := supported(e); err != nil {
		return HostReport{}, err
	}
	r := HostReport{Disks: []Mount{}, GPUs: []GPU{}, Warnings: []string{}}
	var err error
	if r.Host, err = CollectHost(ctx, e); err != nil {
		r.Warnings = append(r.Warnings, "host information incomplete")
	}
	if r.CPU, err = CollectCPU(ctx, e); err != nil {
		r.Warnings = append(r.Warnings, "cpu information unavailable")
	}
	if r.Memory, err = CollectMemory(ctx, e); err != nil {
		r.Warnings = append(r.Warnings, "memory information unavailable")
	}
	if d, err := CollectDisk(ctx, e, ""); err != nil {
		r.Warnings = append(r.Warnings, "disk information unavailable")
	} else {
		r.Disks = d.Mounts
	}
	r.GPUs = collectGPUs(ctx, e)
	return r, nil
}
