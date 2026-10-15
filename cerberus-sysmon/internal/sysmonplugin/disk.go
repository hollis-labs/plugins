package sysmonplugin

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var pseudoFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true,
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true,
	"devpts": true, "efivarfs": true, "ramfs": true, "bpf": true,
	"tracefs": true, "securityfs": true, "debugfs": true, "configfs": true,
	"autofs": true, "mqueue": true, "hugetlbfs": true, "pstore": true,
	"fusectl": true, "nsfs": true, "binfmt_misc": true, "fuse.portal": true,
	"fuse.gvfsd-fuse": true, "fuse.lxcfs": true, "devfs": true,
}

// parseDF parses `df -kP` (hasType false, macOS) or `df -kPT` (hasType true,
// GNU) output. Sizes are 1024-byte blocks. Mount points may contain spaces.
func parseDF(out []byte, hasType bool) ([]Mount, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 1 || !strings.HasPrefix(lines[0], "Filesystem") {
		return nil, fmt.Errorf("unrecognised df output")
	}
	fixed := 6 // device, 1K-blocks, used, avail, capacity, mount
	if hasType {
		fixed = 7
	}
	mounts := []Mount{}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < fixed {
			continue
		}
		m := Mount{Device: f[0]}
		i := 1
		if hasType {
			m.FSType = f[1]
			i = 2
		}
		size, e1 := strconv.ParseUint(f[i], 10, 64)
		used, e2 := strconv.ParseUint(f[i+1], 10, 64)
		avail, e3 := strconv.ParseUint(f[i+2], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		m.SizeBytes, m.UsedBytes, m.AvailBytes = size*1024, used*1024, avail*1024
		if used+avail > 0 {
			m.UsedPct = math.Round(float64(used)/float64(used+avail)*1000) / 10
		}
		m.Mount = strings.Join(f[i+4:], " ")
		mounts = append(mounts, m)
	}
	return mounts, nil
}

// realMounts drops pseudo filesystems and duplicate views of one device.
func realMounts(goos string, in []Mount) []Mount {
	out := []Mount{}
	seen := map[string]bool{}
	for _, m := range in {
		if m.SizeBytes == 0 {
			continue
		}
		if goos == "darwin" {
			if !strings.HasPrefix(m.Device, "/dev/") {
				continue
			}
			if strings.HasPrefix(m.Mount, "/System/Volumes/") && m.Mount != "/System/Volumes/Data" {
				continue
			}
		} else {
			if pseudoFS[m.FSType] {
				continue
			}
			if strings.HasPrefix(m.Device, "/dev/") {
				if seen[m.Device] {
					continue
				}
				seen[m.Device] = true
			}
		}
		out = append(out, m)
	}
	return out
}

func collectDisks(ctx context.Context, e Env) ([]Mount, error) {
	hasType := e.GOOS != "darwin"
	args := []string{"-kP"}
	if hasType {
		args = []string{"-kPT"}
	}
	out, err := e.Run(ctx, "df", args...)
	if err != nil {
		return nil, fmt.Errorf("df unavailable")
	}
	all, err := parseDF(out, hasType)
	if err != nil {
		return nil, err
	}
	return realMounts(e.GOOS, all), nil
}

// collectDiskPath reports the filesystem holding path.
func collectDiskPath(ctx context.Context, e Env, path string) ([]Mount, error) {
	if strings.HasPrefix(path, "-") || strings.ContainsRune(path, 0) {
		return nil, fmt.Errorf("invalid path")
	}
	hasType := e.GOOS != "darwin"
	args := []string{"-kP", path}
	if hasType {
		args = []string{"-kPT", path}
	}
	out, err := e.Run(ctx, "df", args...)
	if err != nil {
		return nil, fmt.Errorf("df failed for the given path")
	}
	return parseDF(out, hasType)
}
