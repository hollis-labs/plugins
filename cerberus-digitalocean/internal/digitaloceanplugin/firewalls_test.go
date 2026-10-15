package digitaloceanplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/digitalocean/godo"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

func tcp(ports string, addrs ...string) map[string]any {
	list := make([]any, len(addrs))
	for i, a := range addrs {
		list[i] = a
	}
	return map[string]any{"protocol": "tcp", "ports": ports, "addresses": list}
}

func dryPreview(t *testing.T, p *Plugin, op string, args map[string]any) DryRunPreview {
	t.Helper()
	args[argDryRun] = true
	return decode[DryRunPreview](t, mustCall(t, p, op, args))
}

// The loud case. Every spelling of "the whole internet", on any port, makes the
// summary and the warnings say so and restate the tailnet-only posture, and the
// preview still does not refuse or call DigitalOcean.
func TestWorldOpenInboundIsFlaggedLoudly(t *testing.T) {
	for _, addr := range []string{"0.0.0.0/0", "::/0", " 0.0.0.0/0 "} {
		for _, ports := range []string{"22", "443", "0", "all", "8000-8100"} {
			for _, op := range []string{"create_firewall", "update_firewall"} {
				backend := &fakeBackend{}
				args := map[string]any{"name": "web", "inbound_rules": []any{tcp(ports, "198.51.100.0/24"), tcp(ports, addr)}}
				if op == "update_firewall" {
					args["firewall_id"] = exampleFirewallID
				}
				p := preview(t, backend, op, args)
				if !strings.HasPrefix(p.Summary, "WORLD-OPEN FIREWALL:") || !strings.Contains(p.Summary, "ENTIRE INTERNET") {
					t.Errorf("%s %s %s: summary %q is not loud", op, addr, ports, p.Summary)
				}
				joined := strings.Join(p.Warnings, "\n")
				if !strings.Contains(joined, "WORLD-OPEN: inbound rule 2") || !strings.Contains(joined, "tailnet-only") || !strings.Contains(joined, "H2") {
					t.Errorf("%s %s %s: warnings = %q", op, addr, ports, p.Warnings)
				}
				if strings.Contains(joined, "inbound rule 1") {
					t.Errorf("a private CIDR was flagged: %q", p.Warnings)
				}
				if len(backend.calls) != 0 {
					t.Errorf("preview called the backend: %+v", backend.calls)
				}
			}
		}
	}
}

func preview(t *testing.T, backend *fakeBackend, op string, args map[string]any) DryRunPreview {
	t.Helper()
	return dryPreview(t, loadedPlugin(t, backend), op, args)
}

func TestPrivateRulesAreNotFlagged(t *testing.T) {
	p := preview(t, &fakeBackend{}, "create_firewall", map[string]any{
		"name": "ok", "inbound_rules": []any{tcp("22", "100.64.0.0/10", "10.0.0.0/8", "203.0.113.7")},
		"outbound_rules": []any{tcp("443", "0.0.0.0/0")}, // egress to anywhere is normal
	})
	if strings.Contains(p.Summary, "WORLD-OPEN") || len(p.Warnings) != 0 {
		t.Fatalf("preview = %+v, want no alarm", p)
	}
}

// Operators may need a public service. The plugin makes it loud, never silent
// and never refused: with --ack it goes through, and the result says world_open.
func TestWorldOpenIsAllowedWhenAcknowledged(t *testing.T) {
	backend := &fakeBackend{}
	args := map[string]any{"name": "web", "inbound_rules": []any{tcp("443", "0.0.0.0/0", "::/0")}, argAcknowledged: true}
	got := decode[Firewall](t, mustCall(t, loadedPlugin(t, backend), "create_firewall", args))
	if !got.WorldOpen || backend.calls[0].method != "CreateFirewall" {
		t.Fatalf("result = %+v, calls = %+v", got, backend.calls)
	}
}

func TestCreateWithNoOutboundWarnsAboutEgress(t *testing.T) {
	p := preview(t, &fakeBackend{}, "create_firewall", map[string]any{"name": "x"})
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "outbound") {
		t.Fatalf("warnings = %q", p.Warnings)
	}
}

func TestListAndGetFirewallComputeWorldOpen(t *testing.T) {
	backend := &fakeBackend{firewalls: []Firewall{
		{ID: "a", Name: "closed", InboundRules: []FirewallRule{{Protocol: "tcp", Ports: "22", Addresses: []string{"100.64.0.0/10"}}}},
		{ID: "b", Name: "open", InboundRules: []FirewallRule{{Protocol: "tcp", Ports: "22", Addresses: []string{"::/0"}}}},
	}}
	p := loadedPlugin(t, backend)
	list := decode[FirewallList](t, mustCall(t, p, "list_firewalls", nil))
	if len(list.Firewalls) != 2 || list.Firewalls[0].WorldOpen || !list.Firewalls[1].WorldOpen {
		t.Fatalf("list = %+v", list)
	}
	got := decode[Firewall](t, mustCall(t, p, "get_firewall", map[string]any{"firewall_id": "b"}))
	if !got.WorldOpen {
		t.Fatalf("get = %+v", got)
	}
}

// DigitalOcean's update is a full replace. Changing only the rules must carry
// the existing droplets, tags and name over, or the update silently detaches
// every droplet from the firewall.
func TestUpdateFirewallKeepsWhatWasNotGiven(t *testing.T) {
	backend := &fakeBackend{firewalls: []Firewall{{
		ID: "fw1", Name: "keep-me", DropletIDs: []int{1, 2}, Tags: []string{"web"},
		OutboundRules: []FirewallRule{{Protocol: "tcp", Ports: "443", Addresses: []string{"0.0.0.0/0"}}},
	}}}
	args := map[string]any{"firewall_id": "fw1", "inbound_rules": []any{tcp("22", "100.64.0.0/10")}, argAcknowledged: true}
	mustCall(t, loadedPlugin(t, backend), "update_firewall", args)
	last := backend.calls[len(backend.calls)-1]
	spec := last.args[1].(FirewallSpec)
	if last.method != "UpdateFirewall" || spec.Name != "keep-me" || !reflect.DeepEqual(spec.DropletIDs, []int{1, 2}) ||
		!reflect.DeepEqual(spec.Tags, []string{"web"}) || len(spec.OutboundRules) != 1 || len(spec.InboundRules) != 1 {
		t.Fatalf("update sent %+v", spec)
	}
}

// An explicitly empty list clears; an absent one keeps.
func TestUpdateFirewallEmptyListClears(t *testing.T) {
	backend := &fakeBackend{firewalls: []Firewall{{ID: "fw1", Name: "n", DropletIDs: []int{1},
		InboundRules: []FirewallRule{{Protocol: "tcp", Ports: "22", Addresses: []string{"0.0.0.0/0"}}}}}}
	mustCall(t, loadedPlugin(t, backend), "update_firewall", map[string]any{"firewall_id": "fw1", "inbound_rules": "[]", argAcknowledged: true})
	spec := backend.calls[len(backend.calls)-1].args[1].(FirewallSpec)
	if len(spec.InboundRules) != 0 || len(spec.DropletIDs) != 1 {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestUpdateFirewallPreviewSaysWhatChanges(t *testing.T) {
	p := preview(t, &fakeBackend{}, "update_firewall", map[string]any{"firewall_id": "fw1", "tags": []any{"web"}})
	changing := p.Input["changing"].(map[string]any)
	if changing["tags"] != true || changing["inbound_rules"] != false || changing["name"] != false {
		t.Fatalf("changing = %+v", changing)
	}
}

func TestUpdateFirewallNeedsSomethingToChange(t *testing.T) {
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "update_firewall", map[string]any{"firewall_id": "fw1", argAcknowledged: true})
	if code, msg := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs || !strings.Contains(msg, "at least one") {
		t.Fatalf("%s %q", code, msg)
	}
}

func TestFirewallArgumentsAreValidated(t *testing.T) {
	bad := map[string]map[string]any{
		"unknown protocol":    {"name": "x", "inbound_rules": []any{map[string]any{"protocol": "gre", "addresses": []any{"10.0.0.0/8"}}}},
		"typo'd field":        {"name": "x", "inbound_rules": []any{map[string]any{"protocol": "tcp", "ports": "22", "address": []any{"10.0.0.0/8"}}}},
		"no source":           {"name": "x", "inbound_rules": []any{map[string]any{"protocol": "tcp", "ports": "22"}}},
		"bad address":         {"name": "x", "inbound_rules": []any{tcp("22", "anywhere")}},
		"bad port":            {"name": "x", "inbound_rules": []any{tcp("99999", "10.0.0.0/8")}},
		"tcp without ports":   {"name": "x", "inbound_rules": []any{tcp("", "10.0.0.0/8")}},
		"not a list":          {"name": "x", "inbound_rules": "nonsense"},
		"missing name":        {"inbound_rules": []any{}},
		"icmp with ports":     {"name": "x", "inbound_rules": []any{map[string]any{"protocol": "icmp", "ports": "1", "addresses": []any{"10.0.0.0/8"}}}},
		"droplet id negative": {"name": "x", "droplet_ids": []any{float64(-3)}},
	}
	for name, args := range bad {
		backend := &fakeBackend{}
		args[argAcknowledged] = true
		result, err := callTool(loadedPlugin(t, backend), "create_firewall", args)
		if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%s: code = %q", name, code)
		}
		if len(backend.calls) != 0 {
			t.Errorf("%s reached the backend", name)
		}
	}
}

// godo joins an id into the request path as text, so an id with a slash would
// address a different endpoint. It is refused before it gets that far.
func TestFirewallIDsCannotEscapeThePath(t *testing.T) {
	for _, id := range []string{"../droplets", "a/b", "a b", "a?x=1", "%2e%2e"} {
		for _, op := range []string{"get_firewall", "delete_firewall", "update_firewall", "assign_firewall_droplets"} {
			backend := &fakeBackend{}
			args := map[string]any{"firewall_id": id, "name": "x", "droplet_ids": []any{float64(1)}, argAcknowledged: true}
			result, err := callTool(loadedPlugin(t, backend), op, args)
			if code, _ := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
				t.Errorf("%s %q: code = %q", op, id, code)
			}
			if len(backend.calls) != 0 {
				t.Errorf("%s %q reached the backend", op, id)
			}
		}
	}
}

func TestDeleteFirewallPreviewNamesWhatIsLost(t *testing.T) {
	p := preview(t, &fakeBackend{}, "delete_firewall", map[string]any{"firewall_id": exampleFirewallID})
	if !strings.Contains(p.Summary, exampleFirewallID) || !strings.Contains(p.Summary, "lose that protection") || len(p.Warnings) == 0 {
		t.Fatalf("preview = %+v", p)
	}
	backend := &fakeBackend{}
	got := decode[ChangeResult](t, mustCall(t, loadedPlugin(t, backend), "delete_firewall", map[string]any{"firewall_id": exampleFirewallID, argAcknowledged: true}))
	if !reflect.DeepEqual(got, ChangeResult{Resource: "firewall", ID: exampleFirewallID, Action: "delete"}) || backend.calls[0].method != "DeleteFirewall" {
		t.Fatalf("got %+v calls %+v", got, backend.calls)
	}
}

func TestAssignAndUnassignFirewallDroplets(t *testing.T) {
	for op, method := range map[string]string{"assign_firewall_droplets": "AddFirewallDroplets", "unassign_firewall_droplets": "RemoveFirewallDroplets"} {
		backend := &fakeBackend{}
		args := map[string]any{"firewall_id": "fw1", "droplet_ids": "1, 2", argAcknowledged: true}
		mustCall(t, loadedPlugin(t, backend), op, args)
		if backend.calls[0].method != method || !reflect.DeepEqual(backend.calls[0].args[1], []int{1, 2}) {
			t.Errorf("%s: calls = %+v", op, backend.calls)
		}
		p := preview(t, &fakeBackend{}, op, map[string]any{"firewall_id": "fw1", "droplet_ids": []any{float64(1)}})
		if !strings.Contains(p.Summary, "[1]") || !strings.Contains(p.Summary, "fw1") {
			t.Errorf("%s preview = %q", op, p.Summary)
		}
	}
}

// The descriptions of every operation that can widen a firewall carry the
// intended posture, so an operator reading the contract sees it.
func TestFirewallWriteDescriptionsStateTheTailnetPosture(t *testing.T) {
	for _, op := range Definition().Operations {
		if op.Name != "create_firewall" && op.Name != "update_firewall" {
			continue
		}
		if !strings.Contains(op.Description, "tailnet-only") || !strings.Contains(op.Description, "H2") || !strings.Contains(op.Description, "0.0.0.0/0") {
			t.Errorf("%s description = %q", op.Name, op.Description)
		}
	}
}

// The recorded request shape: godo gets empty lists, never nulls (an update is
// a full replace), and the response maps onto the DTO without pending changes.
func TestSDKBackendFirewallRoundTrip(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"firewall":{"id":"fw1","name":"n","status":"waiting","created_at":"2026-09-01T12:00:00Z",
		 "inbound_rules":[{"protocol":"tcp","ports":"22","sources":{"addresses":["::/0"]}}],"outbound_rules":[],"droplet_ids":[7],"tags":["t"],
		 "pending_changes":[{"droplet_id":7,"removing":false,"status":"SENTINEL-PENDING-1f2e"}]}}`)
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	b := newSDKBackendWithClient(client)
	got, err := b.UpdateFirewall(context.Background(), "fw1", FirewallSpec{Name: "n"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"inbound_rules", "outbound_rules", "droplet_ids", "tags"} {
		if list, ok := body[key].([]any); !ok || len(list) != 0 {
			t.Errorf("request %s = %#v, want an empty list, not null", key, body[key])
		}
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "SENTINEL-PENDING") || !got.WorldOpen || got.InboundRules[0].Addresses[0] != "::/0" || got.DropletIDs[0] != 7 {
		t.Fatalf("mapping = %s", encoded)
	}
}
