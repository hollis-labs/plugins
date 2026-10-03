package main

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hollis-labs/nanite/pkg/pluginapi"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Literal golden expectations transcribed from Nanite 81f8d3f2:
// internal/chat/context_client.go:330-376. That builder had no demotion logic.
func TestHistoricPinBodyGoldens(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []pin
		want string
	}{
		{"empty", nil, ""},
		{"session", []pin{{Scope: "session", Content: "pin"}}, "[pinned] pin"},
		{"project", []pin{{Scope: "project", Content: "pin"}}, "[pinned:project] pin"},
		{"mixed", []pin{{Scope: "session", Content: "first"}, {Scope: "project", Content: "second"}}, "[pinned] first\n[pinned:project] second"},
		{"whitespace", []pin{{Scope: "session", Content: "  first\nsecond  \n"}, {Scope: "project", Content: "\tthird "}}, "[pinned]   first\nsecond  \n\n[pinned:project] \tthird "},
		{"turn", []pin{{Scope: "turn", Content: "ephemeral"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := renderPins(tc.rows, 6000)
			if err != nil || got != tc.want {
				t.Fatalf("body=%q error=%v; want %q", got, err, tc.want)
			}
		})
	}
	// Pin body composed with historic headings/separators and document formats.
	// This is a text parity golden, not a claim that the plugin owns documents.
	body, _ := renderPins([]pin{{Scope: "session", Content: "remember"}}, 6000)
	got := "## Session Context\nuser note\n\n## Session Documents\n### Document: Full\nbody\n\n### Document: Pointer (pointer)\n(document ID: doc-p, size: 12 bytes)\n\n## Pinned Context\n" + body
	want := "## Session Context\nuser note\n\n## Session Documents\n### Document: Full\nbody\n\n### Document: Pointer (pointer)\n(document ID: doc-p, size: 12 bytes)\n\n## Pinned Context\n[pinned] remember"
	if got != want {
		t.Fatalf("composed golden: %q", got)
	}
}

func TestNewOldestFirstDemotion(t *testing.T) {
	rows := []pin{{Scope: "session", Content: strings.Repeat("a", 200)}, {Scope: "project", Content: strings.Repeat("b", 200)}, {Scope: "session", Content: "newest"}}
	original := append([]pin(nil), rows...)
	full := "[pinned] " + strings.Repeat("a", 200) + "\n[pinned:project] " + strings.Repeat("b", 200) + "\n[pinned] newest"
	for _, tc := range []struct {
		allowance int
		want      string
		fail      bool
	}{
		{len(full), full, false},
		{len(full) - 1, "[pinned:project] " + strings.Repeat("b", 200) + "\n[pinned] newest\n" + omissionLine(1), false},
		{len("[pinned] newest\n") + len(omissionLine(2)), "[pinned] newest\n" + omissionLine(2), false},
		{len("[pinned] newest\n") + len(omissionLine(2)) - 1, omissionLine(3), false},
		{len(omissionLine(3)), omissionLine(3), false},
		{len(omissionLine(3)) - 1, "", true},
		{1, "", true},
	} {
		got, err := renderPins(rows, tc.allowance)
		if (err != nil) != tc.fail || got != tc.want || len(got) > tc.allowance {
			t.Fatalf("allowance %d: %q, %v; want %q fail=%v", tc.allowance, got, err, tc.want, tc.fail)
		}
	}
	if !reflect.DeepEqual(rows, original) {
		t.Fatal("demotion mutated pins")
	}
	// A huge newest pin cannot be skipped to squeeze older pins back in.
	got, err := renderPins([]pin{{Scope: "session", Content: "older"}, {Scope: "session", Content: strings.Repeat("new", 3000)}}, 200)
	if err != nil || got != omissionLine(2) {
		t.Fatal("not a suffix", got, err)
	}
	for _, content := range []string{"bad\x00pin", string([]byte{0xff})} {
		if _, err = renderPins([]pin{{Scope: "session", Content: content}}, 6000); err == nil {
			t.Fatal("invalid content silently omitted")
		}
	}
	// Counts with two digits and multibyte content still obey exact byte budgets.
	many := make([]pin, 12)
	for i := range many {
		many[i] = pin{Scope: "session", Content: strings.Repeat("界", 300)}
	}
	got, err = renderPins(many, len(omissionLine(12)))
	if err != nil || got != omissionLine(12) {
		t.Fatal("count/UTF-8", got, err)
	}
}

func TestAlwaysShipDeclarationAndPrivateFetch(t *testing.T) {
	m, err := declaration()
	if err != nil {
		t.Fatal(err)
	}
	block, err := pluginapi.DecodeBlock(m.Nanite)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pluginapi.AlwaysShipScopeFor(block, m.Capabilities, m.Tools)
	if err != nil || scope.MaxBytes != 6000 || !scope.AllSessions || len(block.Registers.ContextSources) != 0 || len(block.Registers.AlwaysShipSources) != 1 || m.Version != "0.2.0" || m.Hosts["nanite"].Min != "0.1.8" {
		t.Fatal("declaration mismatch", m, scope, err)
	}
	for _, request := range m.Capabilities {
		if request.Name == pluginapi.CapabilityContextSource {
			t.Fatal("dual placement grant")
		}
	}
	params, _ := hostFixture(t, t.TempDir(), true)
	p := &pinsPlugin{}
	if _, err = p.Init(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	request := pluginapi.AlwaysShipRequest{Protocol: 1, SourceID: "pins", SessionID: "session-a", MaxBytes: 6000}
	body, _ := json.Marshal(request)
	valid := subprocess.HTTPRequest{Method: "POST", Path: pluginapi.AlwaysShipFetchPath, SessionID: "session-a", Body: body}
	for _, intent := range []string{"review_session", "recall_decision", "resume_task"} {
		request.Intent = intent
		valid.Body, _ = json.Marshal(request)
		response, fetchErr := p.HTTPHandle(context.Background(), valid)
		decoded, decodeErr := pluginapi.DecodeAlwaysShipResponse(response.Body, 6000)
		if fetchErr != nil || decodeErr != nil || response.Status != 200 || decoded.Body != "[pinned:project] project pin\n[pinned] retained pin" {
			t.Fatal("intent body", intent, response, fetchErr, decodeErr)
		}
	}
	for _, mutate := range []func(*subprocess.HTTPRequest){
		func(c *subprocess.HTTPRequest) { c.Method = "GET" },
		func(c *subprocess.HTTPRequest) { c.RawQuery = "q=private" },
		func(c *subprocess.HTTPRequest) { c.SessionID = "session-b" },
		func(c *subprocess.HTTPRequest) {
			c.Body = []byte(`{"protocol":2,"source_id":"pins","session_id":"session-a","max_bytes":100}`)
		},
		func(c *subprocess.HTTPRequest) {
			c.Body = []byte(`{"protocol":1,"source_id":"other","session_id":"session-a","max_bytes":100}`)
		},
		func(c *subprocess.HTTPRequest) {
			c.Body = []byte(`{"protocol":1,"source_id":"pins","session_id":"session-a","max_bytes":100,"query":"private"}`)
		},
		func(c *subprocess.HTTPRequest) {
			c.Body = []byte(`{"protocol":1,"source_id":"pins","session_id":"session-a","max_bytes":100,"max_bytes":6000}`)
		},
		func(c *subprocess.HTTPRequest) {
			c.Body = []byte(`{"protocol":1,"source_id":"pins","session_id":"session-a","max_bytes":1}`)
		},
	} {
		bad := valid
		mutate(&bad)
		response, fetchErr := p.HTTPHandle(context.Background(), bad)
		if fetchErr != nil || response.Status < 400 {
			t.Fatal("invalid private fetch accepted", response, fetchErr)
		}
	}
	if err = p.Unload(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, err := p.HTTPHandle(context.Background(), valid)
	if err != nil || response.Status != http.StatusServiceUnavailable {
		t.Fatal("unloaded source", response, err)
	}
}

func TestAlwaysShipEmptyAndDeniedQuery(t *testing.T) {
	p := readPlugin(t, nil)
	request := pluginapi.AlwaysShipRequest{Protocol: 1, SourceID: "pins", SessionID: "session-a", MaxBytes: 1}
	body, _ := json.Marshal(request)
	call := subprocess.HTTPRequest{Method: "POST", Path: pluginapi.AlwaysShipFetchPath, SessionID: "session-a", Body: body}
	response, err := p.HTTPHandle(context.Background(), call)
	if err != nil || response.Status != 200 || string(response.Body) != `{"protocol":1,"body":""}` {
		t.Fatal("not explicit empty success", response, err)
	}
	// Even with an imported/cached receipt, a denied session metadata query
	// must fail the fetch and preserve the host's fallback.
	params, _ := hostFixture(t, t.TempDir(), true)
	grant, err := pluginapi.QueryGrantFromIdentity(params.Identity)
	if err != nil {
		t.Fatal(err)
	}
	grant.Token = strings.Repeat("x", len(grant.Token))
	p.client, err = pluginapi.NewQueryClient(grant, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = p.HTTPHandle(context.Background(), call)
	if err != nil || response.Status != http.StatusServiceUnavailable {
		t.Fatal("denied query became empty success", response, err)
	}
	if _, err = p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "pins_list", SessionID: "session-a"}); err == nil {
		t.Fatal("denied query leaked inventory")
	}
}
