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

func TestDropletDTOCarriesVPCVolumesAndTagsOnly(t *testing.T) {
	d := dropletFromSDK(fullyPopulatedDroplet())
	if d.VPCUUID != sentinelVPC || !reflect.DeepEqual(d.VolumeIDs, []string{sentinelVolume}) || !reflect.DeepEqual(d.Tags, []string{sentinelTag}) {
		t.Fatalf("droplet = %+v", d)
	}
	// Absent, the new fields are omitted rather than null.
	encoded, _ := json.Marshal(dropletFromSDK(&godo.Droplet{ID: 1}))
	for _, key := range []string{"vpc_uuid", "volume_ids", "tags"} {
		if strings.Contains(string(encoded), key) {
			t.Errorf("empty droplet carries %s: %s", key, encoded)
		}
	}
}

func TestSSHKeyAddPreviewNeverCarriesTheKeyAndRefusesPrivateKeys(t *testing.T) {
	p := preview(t, &fakeBackend{}, "add_ssh_key", map[string]any{"name": "laptop", "public_key": exampleKey})
	encoded, _ := json.Marshal(p)
	if strings.Contains(string(encoded), "AAAAC3Nza") || p.Input["key_type"] != "ssh-ed25519" {
		t.Fatalf("preview = %s", encoded)
	}
	for name, value := range map[string]string{
		"private":   "-----BEGIN OPENSSH PRIVATE KEY-----\nSENTINEL-PRIVATE-9a8b\n-----END OPENSSH PRIVATE KEY-----",
		"garbage":   "hello world",
		"type only": "ssh-ed25519",
	} {
		backend := &fakeBackend{}
		result, err := callTool(loadedPlugin(t, backend), "add_ssh_key", map[string]any{"name": "k", "public_key": value, argAcknowledged: true})
		code, message := failure(t, result, err)
		if code != cerbplugin.ErrorInvalidArgs || strings.Contains(message, "SENTINEL") || strings.Contains(string(result.Content), "SENTINEL") {
			t.Errorf("%s: %s %q", name, code, message)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s reached the backend", name)
		}
	}
	backend := &fakeBackend{}
	got := decode[SSHKey](t, mustCall(t, loadedPlugin(t, backend), "add_ssh_key", validArgs("add_ssh_key")))
	if got.ID != 5 || backend.calls[0].method != "AddSSHKey" {
		t.Fatalf("got %+v calls %+v", got, backend.calls)
	}
}

func TestDeleteSSHKeyPreviewNamesTheConsequence(t *testing.T) {
	p := preview(t, &fakeBackend{}, "delete_ssh_key", map[string]any{"key": "aa:bb:cc"})
	if !strings.Contains(p.Summary, "aa:bb:cc") || !strings.Contains(p.Summary, "authorized_keys") {
		t.Fatalf("summary = %q", p.Summary)
	}
}

func TestAssignTag(t *testing.T) {
	backend := &fakeBackend{}
	mustCall(t, loadedPlugin(t, backend), "assign_tag", map[string]any{"tag": "web", "resource_type": "Volume", "resource_ids": []any{"a", "b"}, argAcknowledged: true})
	if !reflect.DeepEqual(backend.calls[0].args, []any{"web", "volume", []string{"a", "b"}}) {
		t.Fatalf("calls = %+v", backend.calls)
	}
	p := preview(t, &fakeBackend{}, "assign_tag", map[string]any{"tag": "web", "resource_type": "droplet", "resource_ids": []any{"1", "2"}})
	if !strings.Contains(p.Summary, "firewall") {
		t.Fatalf("preview should warn that firewalls follow tags: %q", p.Summary)
	}
}

func TestAccountArgumentsAreValidated(t *testing.T) {
	bad := []struct {
		op   string
		args map[string]any
	}{
		{"assign_tag", map[string]any{"tag": "has space", "resource_type": "droplet", "resource_ids": []any{"1"}}},
		{"assign_tag", map[string]any{"tag": "web", "resource_type": "firewall", "resource_ids": []any{"1"}}},
		{"assign_tag", map[string]any{"tag": "web", "resource_type": "droplet"}},
		{"assign_tag", map[string]any{"tag": "web", "resource_type": "droplet", "resource_ids": []any{"../1"}}},
		{"assign_project_resources", map[string]any{"project_id": exampleProjectID, "urns": []any{"droplet:1"}}},
		{"assign_project_resources", map[string]any{"project_id": "a/b", "urns": []any{"do:droplet:1"}}},
		{"assign_reserved_ip", map[string]any{"ip": "not-an-ip", "droplet_id": 1}},
		{"assign_reserved_ip", map[string]any{"ip": "203.0.113.10/../x", "droplet_id": 1}},
		{"unassign_reserved_ip", map[string]any{"ip": ""}},
		{"delete_ssh_key", map[string]any{"key": "a/b"}},
		{"get_vpc", map[string]any{"vpc_id": "../x"}},
	}
	for _, tc := range bad {
		backend := &fakeBackend{}
		tc.args[argAcknowledged] = true
		result, err := callTool(loadedPlugin(t, backend), tc.op, tc.args)
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%s %v: code = %q", tc.op, tc.args, code)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s %v reached the backend", tc.op, tc.args)
		}
	}
}

func TestAccountWritesReachTheBackend(t *testing.T) {
	cases := []struct {
		op     string
		method string
		args   []any
	}{
		{"delete_ssh_key", "DeleteSSHKey", []any{"aa:bb:cc"}},
		{"assign_project_resources", "AssignProjectResources", []any{exampleProjectID, []string{"do:droplet:42"}}},
		{"assign_reserved_ip", "AssignReservedIP", []any{"203.0.113.10", 42}},
		{"unassign_reserved_ip", "UnassignReservedIP", []any{"203.0.113.10"}},
		{"get_vpc", "GetVPC", []any{exampleVPCID}},
	}
	for _, tc := range cases {
		backend := &fakeBackend{}
		mustCall(t, loadedPlugin(t, backend), tc.op, validArgs(tc.op))
		if len(backend.calls) != 1 || backend.calls[0].method != tc.method || !reflect.DeepEqual(backend.calls[0].args, tc.args) {
			t.Errorf("%s: calls = %+v, want %s%v", tc.op, backend.calls, tc.method, tc.args)
		}
	}
}

func TestReservedIPPreviewsNameTheDropletAndTheBilling(t *testing.T) {
	assign := preview(t, &fakeBackend{}, "assign_reserved_ip", map[string]any{"ip": "203.0.113.10", "droplet_id": 42})
	unassign := preview(t, &fakeBackend{}, "unassign_reserved_ip", map[string]any{"ip": "203.0.113.10"})
	if !strings.Contains(assign.Summary, "droplet 42") || !strings.Contains(unassign.Summary, "billing") {
		t.Fatalf("assign %q unassign %q", assign.Summary, unassign.Summary)
	}
}

func TestReadsReturnDTOs(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{keys: []SSHKey{{ID: 1, Name: "k", Fingerprint: "f"}}})
	if got := decode[SSHKeyList](t, mustCall(t, p, "list_ssh_keys", nil)); len(got.Keys) != 1 {
		t.Errorf("keys = %+v", got)
	}
	if got := decode[ProjectList](t, mustCall(t, p, "list_projects", nil)); got.Projects[0].ID != exampleProjectID {
		t.Errorf("projects = %+v", got)
	}
	if got := decode[ReservedIPList](t, mustCall(t, p, "list_reserved_ips", nil)); got.ReservedIPs[0].DropletID != 42 {
		t.Errorf("ips = %+v", got)
	}
	if got := decode[VPCList](t, mustCall(t, p, "list_vpcs", nil)); !got.VPCs[0].Default {
		t.Errorf("vpcs = %+v", got)
	}
}

// Recorded shapes through the SDK mapping: a reserved IP's embedded droplet
// record is reduced to its id, keys do not carry the public key, and tagging
// creates a missing tag exactly once.
func TestSDKBackendAccountMapping(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/reserved_ips":
			_, _ = fmt.Fprint(w, `{"reserved_ips":[{"ip":"203.0.113.10","region":{"slug":"nyc3"},"droplet":{"id":42,"name":"SENTINEL-DROPLET-5e2a","networks":{"v4":[{"type":"private","ip_address":"10.9.9.9"}]}},"locked":false,"project_id":"SENTINEL-PROJ"}]}`)
		case r.URL.Path == "/v2/account/keys":
			_, _ = fmt.Fprint(w, `{"ssh_keys":[{"id":3,"name":"k","fingerprint":"aa:bb","public_key":"ssh-ed25519 SENTINEL-PUBKEY-7c1d"}]}`)
		case r.URL.Path == "/v2/tags/web" && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"id":"not_found","message":"nope"}`)
		case r.URL.Path == "/v2/tags" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"tag":{"name":"web"}}`)
		case r.URL.Path == "/v2/tags/web/resources":
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v2/vpcs":
			_, _ = fmt.Fprint(w, `{"vpcs":[{"id":"v1","name":"n","region":"nyc3","ip_range":"10.0.0.0/16","default":true,"created_at":"2026-09-01T12:00:00Z","urn":"do:vpc:v1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	b := newSDKBackendWithClient(client)
	ctx := context.Background()

	ips, err := b.ListReservedIPs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := b.ListSSHKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]any{ips, keys})
	if strings.Contains(string(encoded), "SENTINEL") || strings.Contains(string(encoded), "10.9.9.9") || ips.ReservedIPs[0].DropletID != 42 || keys.Keys[0].Fingerprint != "aa:bb" {
		t.Fatalf("mapping = %s", encoded)
	}
	if err := b.TagResources(ctx, "web", "droplet", []string{"42"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /v2/reserved_ips", "GET /v2/account/keys", "GET /v2/tags/web", "POST /v2/tags", "POST /v2/tags/web/resources"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	vpcs, err := b.ListVPCs(ctx)
	if err != nil || len(vpcs.VPCs) != 1 || vpcs.VPCs[0].IPRange != "10.0.0.0/16" || !vpcs.VPCs[0].Default {
		t.Fatalf("vpcs = %+v, %v", vpcs, err)
	}
}
