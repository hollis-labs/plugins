package digitaloceanplugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/digitalocean/godo"
)

// Sentinels are distinctive so a leak is unambiguous in the serialized output
// rather than a substring of something innocent.
const (
	sentinelPrivateIP = "10.99.88.77"
	sentinelIPv6      = "2001:db8::5e17"
	sentinelTag       = "SENTINEL-DROPLET-TAG-3c1f"
	sentinelVolume    = "SENTINEL-VOLUME-ID-8b4a"
	sentinelVPC       = "SENTINEL-VPC-UUID-2e9d"
	sentinelKernel    = "SENTINEL-KERNEL-7c5b"
	sentinelFeature   = "SENTINEL-FEATURE-1a6f"
	sentinelImageName = "SENTINEL-IMAGE-NAME-4d8e"
	sentinelRegion    = "SENTINEL-REGION-NAME-9f2c"
)

func fullyPopulatedDroplet() *godo.Droplet {
	return &godo.Droplet{
		ID:          42,
		Name:        "web",
		Status:      "active",
		Memory:      1024,
		Vcpus:       1,
		Disk:        25,
		Created:     "2026-09-01T12:00:00Z",
		Region:      &godo.Region{Slug: "nyc3", Name: sentinelRegion},
		Size:        &godo.Size{Slug: "s-1vcpu-1gb"},
		Image:       &godo.Image{Slug: "ubuntu-24-04-x64", Name: sentinelImageName},
		Kernel:      &godo.Kernel{Name: sentinelKernel},
		Tags:        []string{sentinelTag},
		VolumeIDs:   []string{sentinelVolume},
		VPCUUID:     sentinelVPC,
		Features:    []string{sentinelFeature},
		BackupIDs:   []int{880011},
		SnapshotIDs: []int{880022},
		Networks: &godo.Networks{
			V4: []godo.NetworkV4{
				{Type: "private", IPAddress: sentinelPrivateIP},
				{Type: "public", IPAddress: "192.0.2.8"},
			},
			V6: []godo.NetworkV6{{Type: "public", IPAddress: sentinelIPv6}},
		},
	}
}

// The DTO is an allow-list (ADR 0003). A droplet carries its private network,
// tags, volumes and VPC; none of that may reach CLI output, MCP results or an
// agent's context.
func TestDropletDTOOmitsEverythingNotNamed(t *testing.T) {
	encoded, err := json.Marshal(dropletFromSDK(fullyPopulatedDroplet()))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)
	for _, leak := range []string{sentinelPrivateIP, sentinelIPv6, sentinelTag, sentinelVolume, sentinelVPC,
		sentinelKernel, sentinelFeature, sentinelImageName, sentinelRegion, "880011", "880022", "memory", "vcpus"} {
		if strings.Contains(got, leak) {
			t.Fatalf("serialized droplet leaked %q:\n%s", leak, got)
		}
	}
	want := `{"id":42,"name":"web","status":"active","region":"nyc3","size":"s-1vcpu-1gb","image":"ubuntu-24-04-x64","ipv4":"192.0.2.8","created_at":"2026-09-01T12:00:00Z"}`
	if got != want {
		t.Fatalf("droplet =\n %s\nwant (the built-in's field names)\n %s", got, want)
	}
}

func TestDropletWithoutNestedObjectsMapsSafely(t *testing.T) {
	status := dropletFromSDK(&godo.Droplet{ID: 1, Name: "bare", Status: "new"})
	if status.Region != "" || status.IPv4 != "" || !status.CreatedAt.Equal(time.Time{}.UTC()) {
		t.Fatalf("status = %+v", status)
	}
}
