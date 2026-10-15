package sysmonplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// Fixtures are hand-trimmed recordings of the real tools' output. Hostnames
// and addresses are placeholders.

const cpuinfoFixture = `processor	: 0
vendor_id	: GenuineIntel
model name	: Example CPU @ 3.00GHz

processor	: 1
vendor_id	: GenuineIntel
model name	: Example CPU @ 3.00GHz
`

const meminfoFixture = `MemTotal:       16384000 kB
MemFree:         2048000 kB
MemAvailable:    8192000 kB
Buffers:          100000 kB
Cached:          4000000 kB
SwapTotal:       2097152 kB
SwapFree:        2000000 kB
`

const statA = "cpu  1000 0 500 8000 500 0 0 0 0 0\ncpu0 500 0 250 4000 250 0 0 0 0 0\n"
const statB = "cpu  1100 0 550 8300 550 0 0 0 0 0\ncpu0 550 0 275 4150 275 0 0 0 0 0\n"

const dfLinux = `Filesystem     Type     1024-blocks      Used Available Capacity Mounted on
tmpfs          tmpfs         1638400      2000   1636400       1% /run
/dev/nvme0n1p2 ext4        102400000  51200000  46000000      53% /
/dev/nvme0n1p2 ext4        102400000  51200000  46000000      53% /var/lib/bind
/dev/sda1      xfs         512000000 100000000 412000000      20% /mnt/data disk
overlay        overlay      10000000   1000000   9000000      10% /var/lib/containers/x
`

const dfDarwin = `Filesystem       1024-blocks      Used Available Capacity Mounted on
/dev/disk3s1s1     488245288  10000000 200000000     5% /
devfs                    200       200         0   100% /dev
/dev/disk3s6       488245288   5000000 200000000     3% /System/Volumes/VM
/dev/disk3s5       488245288 250000000 200000000    56% /System/Volumes/Data
map auto_home              0         0         0   100% /System/Volumes/Data/home
`

const vmstatFixture = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               10000.
Pages active:                            200000.
Pages inactive:                           50000.
Pages speculative:                         2000.
Pages purgeable:                           1000.
`

const nvidiaFixture = "NVIDIA GeForce RTX 4090, 24564, 550.54.14\nNVIDIA GeForce RTX 4090, 24564, 550.54.14\n"

const rocmFixture = `{"card0": {"Card Series": "Radeon RX 7900 XT", "VRAM Total Memory (B)": "21458059264", "VRAM Total Used Memory (B)": "1000"}, "system": {"Driver version": "6.1"}}`

const appleFixture = `{"SPDisplaysDataType":[{"_name":"Apple M2 Pro","sppci_model":"Apple M2 Pro","spdisplays_vendor":"sppci_vendor_Apple","sppci_cores":"19"}]}`

type fake struct {
	goos   string
	files  map[string][]string // successive reads
	reads  map[string]int
	cmds   map[string]string // "name arg arg" -> output; absent => tool missing
	called []string
}

func (f *fake) env() Env {
	f.reads = map[string]int{}
	return Env{
		GOOS: f.goos, GOARCH: "amd64",
		ReadFile: func(p string) ([]byte, error) {
			seq, ok := f.files[p]
			if !ok {
				return nil, fmt.Errorf("no such file")
			}
			i := f.reads[p]
			if i >= len(seq) {
				i = len(seq) - 1
			}
			f.reads[p]++
			return []byte(seq[i]), nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			key := strings.Join(append([]string{name}, args...), " ")
			f.called = append(f.called, key)
			out, ok := f.cmds[key]
			if !ok {
				return nil, fmt.Errorf("not found")
			}
			return []byte(out), nil
		},
		Hostname: func() (string, error) { return "host.example", nil },
		Now:      func() time.Time { return time.Unix(1700003600, 0) },
		Sleep:    func(time.Duration) {},
	}
}

func linuxFake() *fake {
	return &fake{goos: "linux",
		files: map[string][]string{
			"/proc/cpuinfo":              {cpuinfoFixture},
			"/proc/meminfo":              {meminfoFixture},
			"/proc/loadavg":              {"0.52 0.40 0.30 1/400 12345\n"},
			"/proc/uptime":               {"3600.55 7000.00\n"},
			"/proc/stat":                 {statA, statB},
			"/proc/sys/kernel/osrelease": {"6.8.0-test\n"},
			"/etc/os-release":            {"NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\n"},
		},
		cmds: map[string]string{"df -kPT": dfLinux}}
}

func darwinFake() *fake {
	return &fake{goos: "darwin", files: map[string][]string{},
		cmds: map[string]string{
			"sysctl -n hw.logicalcpu":                  "12\n",
			"sysctl -n machdep.cpu.brand_string":       "Apple M2 Pro\n",
			"sysctl -n vm.loadavg":                     "{ 1.50 1.25 1.00 }\n",
			"sysctl -n hw.memsize":                     "17179869184\n",
			"sysctl -n vm.swapusage":                   "total = 2048.00M  used = 1024.00M  free = 1024.00M  (encrypted)\n",
			"sysctl -n kern.osrelease":                 "23.4.0\n",
			"sysctl -n kern.boottime":                  "{ sec = 1700000000, usec = 0 } Tue Nov 14 2023\n",
			"sw_vers -productVersion":                  "14.4.1\n",
			"vm_stat":                                  vmstatFixture,
			"df -kP":                                   dfDarwin,
			"system_profiler SPDisplaysDataType -json": appleFixture,
		}}
}

func TestLinuxSections(t *testing.T) {
	e := linuxFake().env()
	ctx := context.Background()

	c, err := CollectCPU(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if c.Cores != 2 || c.Model != "Example CPU @ 3.00GHz" || c.Load.One != 0.52 || c.Load.Fifteen != 0.30 {
		t.Fatalf("cpu: %+v", c)
	}
	// busy delta 150, total delta 500 -> 30.0
	if c.UsagePercent == nil || *c.UsagePercent != 30.0 {
		t.Fatalf("usage: %v", c.UsagePercent)
	}

	m, err := CollectMemory(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalBytes != 16384000*1024 || m.AvailableBytes != 8192000*1024 || m.FreeBytes != 2048000*1024 ||
		m.UsedBytes != 8192000*1024 || m.SwapTotalBytes != 2097152*1024 {
		t.Fatalf("memory: %+v", m)
	}

	h, err := CollectHost(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if h.OS != "linux" || h.Arch != "amd64" || h.Kernel != "6.8.0-test" || h.OSVersion != "Ubuntu 24.04 LTS" ||
		h.UptimeSeconds != 3600 || h.Hostname != "host.example" {
		t.Fatalf("host: %+v", h)
	}
}

func TestLinuxDiskFiltersPseudoAndDuplicates(t *testing.T) {
	d, err := CollectDisk(context.Background(), linuxFake().env(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Mounts) != 2 {
		t.Fatalf("mounts: %+v", d.Mounts)
	}
	root := d.Mounts[0]
	if root.Mount != "/" || root.FSType != "ext4" || root.SizeBytes != 102400000*1024 ||
		root.UsedBytes != 51200000*1024 || root.AvailBytes != 46000000*1024 || root.UsedPct != 52.7 {
		t.Fatalf("root: %+v", root)
	}
	if d.Mounts[1].Mount != "/mnt/data disk" {
		t.Fatalf("mount with space: %+v", d.Mounts[1])
	}
}

func TestDiskPathAndRefusal(t *testing.T) {
	f := linuxFake()
	f.cmds["df -kPT /"] = `Filesystem Type 1024-blocks Used Available Capacity Mounted on
/dev/sda1 ext4 1000 400 600 40% /
`
	d, err := CollectDisk(context.Background(), f.env(), "/")
	if err != nil || len(d.Mounts) != 1 || d.Mounts[0].SizeBytes != 1000*1024 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := CollectDisk(context.Background(), f.env(), "-h"); err == nil {
		t.Fatal("option-looking path must be refused")
	}
}

func TestDarwinSections(t *testing.T) {
	e := darwinFake().env()
	ctx := context.Background()

	c, err := CollectCPU(ctx, e)
	if err != nil || c.Cores != 12 || c.Model != "Apple M2 Pro" || c.Load.Five != 1.25 || c.UsagePercent != nil {
		t.Fatalf("cpu: %+v %v", c, err)
	}
	m, err := CollectMemory(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	page := uint64(16384)
	if m.TotalBytes != 17179869184 || m.FreeBytes != 12000*page || m.AvailableBytes != 63000*page ||
		m.SwapTotalBytes != 2048*1048576 || m.SwapFreeBytes != 1024*1048576 {
		t.Fatalf("memory: %+v", m)
	}
	h, err := CollectHost(ctx, e)
	if err != nil || h.OSVersion != "14.4.1" || h.Kernel != "23.4.0" || h.UptimeSeconds != 3600 {
		t.Fatalf("host: %+v %v", h, err)
	}
	d, err := CollectDisk(ctx, e, "")
	if err != nil || len(d.Mounts) != 2 || d.Mounts[1].Mount != "/System/Volumes/Data" {
		t.Fatalf("disk: %+v %v", d, err)
	}
}

func TestGPUs(t *testing.T) {
	ctx := context.Background()

	f := linuxFake()
	f.cmds["nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits"] = nvidiaFixture
	f.cmds["rocm-smi --showproductname --showmeminfo vram --json"] = rocmFixture
	g := collectGPUs(ctx, f.env())
	if len(g) != 3 || g[0].Vendor != "nvidia" || g[0].Driver != "550.54.14" ||
		*g[0].MemoryTotalBytes != 24564<<20 || g[2].Vendor != "amd" || *g[2].MemoryTotalBytes != 21458059264 {
		t.Fatalf("gpus: %+v", g)
	}

	// No tools: empty list, never nil, never an error.
	none := collectGPUs(ctx, linuxFake().env())
	if none == nil || len(none) != 0 {
		t.Fatalf("absent gpus must be an empty list: %#v", none)
	}

	a := collectGPUs(ctx, darwinFake().env())
	if len(a) != 1 || a[0].Vendor != "apple" || a[0].Model != "Apple M2 Pro" || *a[0].Cores != 19 || a[0].MemoryTotalBytes != nil {
		t.Fatalf("apple: %+v", a)
	}
}

func TestMalformedGPUOutputIsEmpty(t *testing.T) {
	if g := parseRocmGPUs([]byte("not json")); len(g) != 0 {
		t.Fatal(g)
	}
	if g := parseAppleGPUs([]byte("{")); len(g) != 0 {
		t.Fatal(g)
	}
	if g := parseNvidiaGPUs([]byte("garbage")); len(g) != 0 {
		t.Fatal(g)
	}
}

func TestReportDegradesWithWarnings(t *testing.T) {
	f := linuxFake()
	delete(f.files, "/proc/meminfo")
	delete(f.cmds, "df -kPT")
	r, err := CollectReport(context.Background(), f.env())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings) != 2 || r.CPU.Cores != 2 || r.Host.Kernel == "" {
		t.Fatalf("report: %+v", r)
	}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"gpus":[]`) || !strings.Contains(string(b), `"disks":[]`) {
		t.Fatalf("lists must serialize as [], got %s", b)
	}
}

func TestUnsupportedOS(t *testing.T) {
	e := linuxFake().env()
	e.GOOS = "windows"
	if _, err := CollectReport(context.Background(), e); err == nil {
		t.Fatal("expected unsupported error")
	}
}

func TestOperationsReturnStructuredContent(t *testing.T) {
	p := NewWithEnv(linuxFake().env())
	for _, tool := range []string{"cpu", "memory", "disk", "host", "host_report"} {
		res, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
			ToolName: "cerberus_sysmon_" + tool, Arguments: map[string]any{},
		})
		if tool == "disk" {
			// default path "/" is not in the fake; the error must be plain guidance.
			if err == nil {
				t.Fatal("expected fake df miss")
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		var obj map[string]any
		if json.Unmarshal(res.Content, &obj) != nil {
			t.Fatalf("%s content is not a JSON object: %s", tool, res.Content)
		}
	}
}

func TestResolveToolUsesFallbackDirs(t *testing.T) {
	t.Setenv("PATH", "")
	// sh lives in /bin or /usr/bin on every supported host.
	if _, err := resolveTool("sh"); err != nil {
		t.Fatalf("fallback search failed: %v", err)
	}
	if _, err := resolveTool("definitely-not-a-real-tool"); err == nil {
		t.Fatal("missing tool must error")
	}
}
