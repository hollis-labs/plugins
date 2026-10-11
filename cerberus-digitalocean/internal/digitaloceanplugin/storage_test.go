package digitaloceanplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// Billable creates say so in the summary and in the contract.
func TestBillableCreatesSaySoInTheirSummary(t *testing.T) {
	cases := map[string]map[string]any{
		"create_droplet":          {"name": "w", "region": "nyc3", "size": "s-1vcpu-1gb", "image": "ubuntu-24-04-x64"},
		"create_volume":           {"name": "data-1", "region": "nyc3", "size_gigabytes": 10},
		"resize_volume":           {"volume_id": exampleVolumeID, "size_gigabytes": 20, "region": "nyc3"},
		"create_volume_snapshot":  {"volume_id": exampleVolumeID, "name": "s"},
		"create_droplet_snapshot": {"droplet_id": 42, "name": "s"},
	}
	for op, args := range cases {
		p := preview(t, &fakeBackend{}, op, args)
		if !strings.Contains(p.Summary, "BILLABLE") {
			t.Errorf("%s summary %q does not say it is billable", op, p.Summary)
		}
		for _, def := range Definition().Operations {
			if def.Name == op && def.Cost != "billable" {
				t.Errorf("%s cost = %s", op, def.Cost)
			}
		}
	}
}

// A stopped droplet still bills; the stop preview and description say so.
func TestStopSaysAStoppedDropletStillBills(t *testing.T) {
	p := preview(t, &fakeBackend{}, "stop", map[string]any{"droplet_id": 42})
	if !strings.Contains(p.Summary, "STILL BILLS") {
		t.Fatalf("summary = %q", p.Summary)
	}
	for _, def := range Definition().Operations {
		if def.Name == "stop" && !strings.Contains(def.Description, "STILL BILLS") {
			t.Fatalf("description = %q", def.Description)
		}
	}
}

func TestDeletePreviewsNameWhatIsLost(t *testing.T) {
	cases := map[string]struct {
		args map[string]any
		id   string
		lost string
	}{
		"delete_volume":   {map[string]any{"volume_id": exampleVolumeID}, exampleVolumeID, "ALL data"},
		"delete_snapshot": {map[string]any{"snapshot_id": exampleSnapshotID}, exampleSnapshotID, "cannot be recovered"},
	}
	for op, tc := range cases {
		backend := &fakeBackend{}
		p := preview(t, backend, op, tc.args)
		if !strings.Contains(p.Summary, tc.id) || !strings.Contains(p.Summary, tc.lost) || len(p.Warnings) == 0 {
			t.Errorf("%s preview = %+v", op, p)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s preview called the backend", op)
		}
	}
}

func TestAttachAndDetachPreviewsNameTheDroplet(t *testing.T) {
	for _, op := range []string{"attach_volume", "detach_volume"} {
		p := preview(t, &fakeBackend{}, op, map[string]any{"volume_id": exampleVolumeID, "droplet_id": "42"})
		if !strings.Contains(p.Summary, "droplet 42") || !strings.Contains(p.Summary, exampleVolumeID) || p.Target["droplet_id"] != float64(42) {
			t.Errorf("%s preview = %+v", op, p)
		}
	}
	p := preview(t, &fakeBackend{}, "detach_volume", map[string]any{"volume_id": exampleVolumeID, "droplet_id": 42})
	if !strings.Contains(p.Summary, "Unmount") {
		t.Errorf("detach does not warn to unmount: %q", p.Summary)
	}
}

func TestVolumeWritesReachTheBackend(t *testing.T) {
	cases := []struct {
		op     string
		method string
		args   []any
	}{
		{"attach_volume", "AttachVolume", []any{exampleVolumeID, 42}},
		{"detach_volume", "DetachVolume", []any{exampleVolumeID, 42}},
		{"resize_volume", "ResizeVolume", []any{exampleVolumeID, 20, "nyc3"}},
		{"delete_volume", "DeleteVolume", []any{exampleVolumeID}},
		{"delete_snapshot", "DeleteSnapshot", []any{exampleSnapshotID}},
		{"create_droplet_snapshot", "CreateDropletSnapshot", []any{42, "before-upgrade"}},
		{"create_volume_snapshot", "CreateVolumeSnapshot", []any{exampleVolumeID, "before-upgrade"}},
	}
	for _, tc := range cases {
		backend := &fakeBackend{}
		mustCall(t, loadedPlugin(t, backend), tc.op, validArgs(tc.op))
		if len(backend.calls) != 1 || backend.calls[0].method != tc.method || !reflect.DeepEqual(backend.calls[0].args, tc.args) {
			t.Errorf("%s: calls = %+v, want %s%v", tc.op, backend.calls, tc.method, tc.args)
		}
	}
	backend := &fakeBackend{}
	got := decode[Volume](t, mustCall(t, loadedPlugin(t, backend), "create_volume", validArgs("create_volume")))
	spec := backend.calls[0].args[0].(VolumeSpec)
	if got.ID != exampleVolumeID || spec.SizeGigabytes != 10 || spec.Name != "data-1" {
		t.Fatalf("got %+v spec %+v", got, spec)
	}
}

func TestVolumeArgumentsAreValidated(t *testing.T) {
	bad := map[string]map[string]any{
		"create_volume":           {"name": "Has Caps", "region": "nyc3", "size_gigabytes": 10},
		"create_volume ":          {"name": "ok", "region": "nyc3", "size_gigabytes": 0},
		"create_volume  ":         {"name": "ok", "region": "nyc3", "size_gigabytes": 99999},
		"create_volume   ":        {"name": "ok", "region": "nyc3"},
		"attach_volume":           {"volume_id": "../x", "droplet_id": 1},
		"attach_volume ":          {"volume_id": exampleVolumeID},
		"delete_volume":           {"volume_id": "a/b"},
		"list_snapshots":          {"resource_type": "image"},
		"create_volume_snapshot":  {"volume_id": exampleVolumeID, "name": "line\nbreak"},
		"create_droplet_snapshot": {"droplet_id": 1, "name": strings.Repeat("x", 300)},
		"delete_snapshot":         {"snapshot_id": "x y"},
	}
	for key, args := range bad {
		op := strings.TrimSpace(key)
		backend := &fakeBackend{}
		args[argAcknowledged] = true
		result, err := callTool(loadedPlugin(t, backend), op, args)
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%q: code = %q", key, code)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%q reached the backend", key)
		}
	}
}

func TestListVolumesAndSnapshots(t *testing.T) {
	backend := &fakeBackend{volumes: []Volume{{ID: "v", Name: "data", DropletIDs: []int{1}}}, snapshots: []Snapshot{{ID: "s", Name: "snap"}}}
	p := loadedPlugin(t, backend)
	vols := decode[VolumeList](t, mustCall(t, p, "list_volumes", map[string]any{"region": "nyc3", "name": "data"}))
	snaps := decode[SnapshotList](t, mustCall(t, p, "list_snapshots", map[string]any{"resource_type": "Droplet"}))
	if len(vols.Volumes) != 1 || len(snaps.Snapshots) != 1 {
		t.Fatalf("%+v %+v", vols, snaps)
	}
	if !reflect.DeepEqual(backend.calls[0].args, []any{"nyc3", "data"}) || backend.calls[1].args[0] != "droplet" {
		t.Fatalf("calls = %+v", backend.calls)
	}
}

// Recorded shapes through the real SDK mapping: volumes drop the filesystem
// label, snapshots carry their regions, and the requests are what DO expects.
func TestSDKBackendStorageMapping(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/volumes" && r.Method == http.MethodGet:
			_, _ = fmt.Fprint(w, `{"volumes":[{"id":"v1","name":"data","region":{"slug":"nyc3"},"size_gigabytes":10,"droplet_ids":[7],"created_at":"2026-09-01T12:00:00Z","filesystem_label":"SENTINEL-FSLABEL-5d1c","tags":["t"]}]}`)
		case r.URL.Path == "/v2/snapshots" && r.URL.Query().Get("resource_type") == "droplet":
			_, _ = fmt.Fprint(w, `{"snapshots":[{"id":"99","name":"s","resource_id":"7","resource_type":"droplet","regions":["nyc3"],"min_disk_size":25,"size_gigabytes":1.5,"created_at":"2026-09-01T12:00:00Z"}]}`)
		case r.URL.Path == "/v2/droplets/7/actions":
			_, _ = fmt.Fprint(w, `{"action":{"id":1,"status":"in-progress","type":"snapshot"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	b := newSDKBackendWithClient(client)
	ctx := context.Background()

	vols, err := b.ListVolumes(ctx, "nyc3", "data")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(vols)
	if strings.Contains(string(encoded), "SENTINEL") || vols.Volumes[0].Region != "nyc3" || vols.Volumes[0].DropletIDs[0] != 7 {
		t.Fatalf("volumes = %s", encoded)
	}
	if !strings.Contains(paths[0], "region=nyc3") || !strings.Contains(paths[0], "name=data") {
		t.Errorf("list request = %s", paths[0])
	}
	snaps, err := b.ListSnapshots(ctx, "droplet")
	if err != nil || len(snaps.Snapshots) != 1 || snaps.Snapshots[0].MinDiskSizeGB != 25 || snaps.Snapshots[0].CreatedAt.IsZero() {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	if err := b.CreateDropletSnapshot(ctx, 7, "s"); err != nil {
		t.Fatal(err)
	}
}
