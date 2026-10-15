package sysmonplugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parseSysctlLoad reads `sysctl -n vm.loadavg`: "{ 1.23 1.10 0.98 }".
func parseSysctlLoad(out []byte) (LoadAverage, error) {
	s := strings.Trim(strings.TrimSpace(string(out)), "{}")
	f := strings.Fields(s)
	if len(f) < 3 {
		return LoadAverage{}, fmt.Errorf("unrecognised vm.loadavg")
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

var bootSecRE = regexp.MustCompile(`sec\s*=\s*(\d+)`)

// parseBootTime reads `sysctl -n kern.boottime`: "{ sec = 1700000000, usec = 0 } ...".
func parseBootTime(out []byte) (int64, error) {
	m := bootSecRE.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("unrecognised kern.boottime")
	}
	return strconv.ParseInt(string(m[1]), 10, 64)
}

var (
	vmPageSizeRE = regexp.MustCompile(`page size of (\d+) bytes`)
	vmLineRE     = regexp.MustCompile(`^Pages ([a-z ]+):\s+(\d+)\.`)
)

// parseVMStat combines `vm_stat` with the hw.memsize total. Free is free plus
// speculative pages; available adds inactive and purgeable pages.
func parseVMStat(out []byte, total uint64) (Memory, error) {
	text := string(out)
	ps := vmPageSizeRE.FindStringSubmatch(text)
	if ps == nil {
		return Memory{}, fmt.Errorf("unrecognised vm_stat")
	}
	page, _ := strconv.ParseUint(ps[1], 10, 64)
	pages := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		if m := vmLineRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			n, _ := strconv.ParseUint(m[2], 10, 64)
			pages[m[1]] = n
		}
	}
	free := (pages["free"] + pages["speculative"]) * page
	avail := free + (pages["inactive"]+pages["purgeable"])*page
	if avail > total {
		avail = total
	}
	m := Memory{TotalBytes: total, FreeBytes: free, AvailableBytes: avail}
	m.UsedBytes = total - avail
	return m, nil
}

var swapRE = regexp.MustCompile(`total = ([\d.]+)M\s+used = ([\d.]+)M\s+free = ([\d.]+)M`)

// parseSwapUsage reads `sysctl -n vm.swapusage`.
func parseSwapUsage(out []byte) (total, free uint64) {
	m := swapRE.FindStringSubmatch(string(out))
	if m == nil {
		return 0, 0
	}
	t, _ := strconv.ParseFloat(m[1], 64)
	f, _ := strconv.ParseFloat(m[3], 64)
	return uint64(t * 1048576), uint64(f * 1048576)
}
