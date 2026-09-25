package k8splugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func newTestPlugin(t *testing.T, backend Backend, config map[string]string) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: config}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func call(t *testing.T, p *Plugin, operation string, args map[string]any) subprocess.MCPCallResult {
	t.Helper()
	result, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	return result
}

// A missing credential must not fail the load. When authentication is broken,
// list_contexts and check_access are the two operations an operator needs, and
// a plugin that refused to load would remove them exactly when they matter.
func TestLoadSucceedsWithNoCredentialConfigured(t *testing.T) {
	p := newTestPlugin(t, &FakeBackend{}, nil)
	if p.backend == nil {
		t.Fatal("backend is nil after Load")
	}
}

// Context selection is per operation, not per process: one daemon serves
// several clusters, and the next call must not inherit the last one's choice.
// This is the invariant the Docker connector learned the hard way in WP-3.
func TestContextSelectionIsPerCallAndDoesNotLeakIntoTheNextOne(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, map[string]string{ConfigContext: "default-ctx"})

	call(t, p, "list_pods", map[string]any{"context": "staging"})
	if fake.LastOptions.Context != "staging" {
		t.Fatalf("context = %q, want the per-call override to reach the backend", fake.LastOptions.Context)
	}

	call(t, p, "list_nodes", nil)
	if fake.LastOptions.Context != "default-ctx" {
		t.Fatalf("context = %q, want the configured default back — the previous call's context leaked", fake.LastOptions.Context)
	}
}

func TestNamespaceFallsBackFromArgumentToConfigToDefault(t *testing.T) {
	fake := &FakeBackend{}

	p := newTestPlugin(t, fake, map[string]string{ConfigNamespace: "configured"})
	call(t, p, "list_pods", map[string]any{"namespace": "explicit"})
	if fake.LastPodQuery.Namespace != "explicit" {
		t.Errorf("namespace = %q, want the argument to win", fake.LastPodQuery.Namespace)
	}
	call(t, p, "list_pods", nil)
	if fake.LastPodQuery.Namespace != "configured" {
		t.Errorf("namespace = %q, want the configured default", fake.LastPodQuery.Namespace)
	}

	// With no argument and no configured namespace, the kubeconfig context's
	// namespace comes before "default" — as it does for kubectl.
	kubeconfig := writeKubeconfig(t, t.TempDir()) // current-context prod, namespace apps
	fromContext := newTestPlugin(t, fake, map[string]string{ConfigKubeconfig: kubeconfig})
	call(t, fromContext, "list_pods", nil)
	if fake.LastPodQuery.Namespace != "apps" {
		t.Errorf("namespace = %q, want the kubeconfig context's", fake.LastPodQuery.Namespace)
	}
	// A per-call context switch changes which context's namespace applies; dev
	// names none, so it falls through to default.
	call(t, fromContext, "list_pods", map[string]any{"context": "dev"})
	if fake.LastPodQuery.Namespace != DefaultNamespace {
		t.Errorf("namespace = %q for a context with none, want %q", fake.LastPodQuery.Namespace, DefaultNamespace)
	}
	// The connector's configured namespace outranks the context's.
	configured := newTestPlugin(t, fake, map[string]string{ConfigKubeconfig: kubeconfig, ConfigNamespace: "configured"})
	call(t, configured, "list_pods", nil)
	if fake.LastPodQuery.Namespace != "configured" {
		t.Errorf("namespace = %q, want the configured namespace over the context's", fake.LastPodQuery.Namespace)
	}

	bare := newTestPlugin(t, fake, nil)
	call(t, bare, "list_pods", nil)
	if fake.LastPodQuery.Namespace != DefaultNamespace {
		t.Errorf("namespace = %q, want %q", fake.LastPodQuery.Namespace, DefaultNamespace)
	}
}

// MCP arguments arrive as decoded JSON, so every number is a float64. Reading
// them as int without the conversion silently yields the zero value, which for
// a log tail would return nothing and look like an empty log.
func TestNumericArgumentsSurviveJSONDecoding(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)

	raw := []byte(`{"pod":"web-1","tail":25,"previous":true}`)
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	call(t, p, "get_logs", args)

	if fake.LastLog.Tail != 25 {
		t.Errorf("tail = %d, want 25", fake.LastLog.Tail)
	}
	if !fake.LastLog.Previous {
		t.Error("previous = false, want true")
	}
	if fake.LastLog.Pod != "web-1" {
		t.Errorf("pod = %q", fake.LastLog.Pod)
	}
}

func TestEventLimitAndLogTailDefaultWhenOmitted(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)

	call(t, p, "list_events", nil)
	if fake.LastEvent.Limit != DefaultEventLimit {
		t.Errorf("event limit = %d, want %d", fake.LastEvent.Limit, DefaultEventLimit)
	}
	call(t, p, "get_logs", map[string]any{"pod": "web-1"})
	if fake.LastLog.Tail != DefaultLogTail {
		t.Errorf("log tail = %d, want %d", fake.LastLog.Tail, DefaultLogTail)
	}
}

func TestGetLogsRequiresAPodName(t *testing.T) {
	p := newTestPlugin(t, &FakeBackend{}, nil)
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, "get_logs"),
		Arguments: map[string]any{},
	})
	if err == nil || !strings.Contains(err.Error(), "pod name") {
		t.Fatalf("error = %v, want a message naming the missing pod", err)
	}
}

// list_contexts and check_access answer from the kubeconfig, so they must not
// reach the backend at all — that is what keeps them working when the cluster
// cannot be contacted.
func TestKubeconfigOperationsDoNotTouchTheBackend(t *testing.T) {
	fake := &FakeBackend{}
	dir := t.TempDir()
	path := writeKubeconfig(t, dir)
	p := newTestPlugin(t, fake, nil)

	call(t, p, "list_contexts", map[string]any{"kubeconfig": path})
	call(t, p, "check_access", map[string]any{"kubeconfig": path, "context": "prod"})

	if len(fake.Calls) != 0 {
		t.Fatalf("backend was called %v, want no cluster contact", fake.Calls)
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	p := newTestPlugin(t, &FakeBackend{}, nil)
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_kubernetes_delete_everything"})
	if err == nil || !strings.Contains(err.Error(), "unsupported tool") {
		t.Fatalf("error = %v, want an unsupported-tool rejection", err)
	}
}

func TestEveryDeclaredOperationIsServed(t *testing.T) {
	fake := &FakeBackend{}
	dir := t.TempDir()
	path := writeKubeconfig(t, dir)
	p := newTestPlugin(t, fake, map[string]string{ConfigKubeconfig: path})

	for _, op := range Definition().Operations {
		// Supply every argument the schema marks required, typed as the schema
		// says, and preview writes rather than applying them.
		args := map[string]any{}
		props, _ := op.InputSchema["properties"].(map[string]any)
		required, _ := op.InputSchema["required"].([]string)
		for _, name := range required {
			prop, _ := props[name].(map[string]any)
			switch {
			case prop["type"] == "integer":
				args[name] = float64(1)
			case name == "kind":
				args[name] = "deployment"
			default:
				args[name] = "x"
			}
		}
		if writeOperations[op.Name] {
			args["dry_run"] = true
		}
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
			ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, op.Name),
			Arguments: args,
		})
		if err != nil {
			t.Errorf("declared operation %q is not served: %v", op.Name, err)
		}
	}
}

// The two surfaces type their arguments differently and the difference is
// silent. Over MCP, `limit` is a JSON number and `all_namespaces` a JSON bool.
// Over the CLI, `--arg limit=2` makes both strings. A parser handling only the
// first typing dropped both: the limit fell back to the default and the
// cluster-wide read quietly became a single-namespace read.
//
// This was found by running the installed plugin through the real CLI, not by
// any test, which is why both typings are asserted here.
func TestArgumentsAcceptBothJSONAndCLIStringTypings(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"mcp json types", map[string]any{"limit": float64(2), "all_namespaces": true}},
		{"cli string types", map[string]any{"limit": "2", "all_namespaces": "true"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &FakeBackend{}
			p := newTestPlugin(t, fake, nil)
			call(t, p, "list_pods", tc.args)

			if fake.LastPodQuery.Limit != 2 {
				t.Errorf("limit = %d, want 2 — a dropped limit returns far more than asked for", fake.LastPodQuery.Limit)
			}
			if !fake.LastPodQuery.AllNamespaces {
				t.Error("all_namespaces = false; an operator asking for the whole cluster got one namespace")
			}
		})
	}
}

func TestStringBooleanVariantsAreAccepted(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "1", "t"} {
		fake := &FakeBackend{}
		p := newTestPlugin(t, fake, nil)
		call(t, p, "list_pods", map[string]any{"all_namespaces": value})
		if !fake.LastPodQuery.AllNamespaces {
			t.Errorf("all_namespaces=%q was not accepted", value)
		}
	}
	for _, value := range []string{"false", "0", "f", ""} {
		fake := &FakeBackend{}
		p := newTestPlugin(t, fake, nil)
		call(t, p, "list_pods", map[string]any{"all_namespaces": value})
		if fake.LastPodQuery.AllNamespaces {
			t.Errorf("all_namespaces=%q was read as true", value)
		}
	}
}

// A malformed value must fail loudly. Silently falling back to the default is
// the same defect as dropping the argument: the caller asked for something
// specific and got something else with no signal.
func TestMalformedArgumentsAreRejectedRatherThanDefaulted(t *testing.T) {
	cases := []struct {
		operation string
		args      map[string]any
		want      string
	}{
		{"list_pods", map[string]any{"limit": "abc"}, "whole number"},
		{"list_pods", map[string]any{"all_namespaces": "yep"}, "true or false"},
		{"list_events", map[string]any{"limit": "1e6"}, "whole number"},
		{"get_logs", map[string]any{"pod": "web-1", "tail": "lots"}, "whole number"},
		{"list_namespaces", map[string]any{"limit": []any{1}}, "whole number"},
	}
	for _, tc := range cases {
		fake := &FakeBackend{}
		p := newTestPlugin(t, fake, nil)
		_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
			ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, tc.operation),
			Arguments: tc.args,
		})
		if err == nil {
			t.Errorf("%s %v: want an error, got none", tc.operation, tc.args)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %v: error %q does not explain the expected type", tc.operation, tc.args, err)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("%s %v: backend was called despite bad arguments", tc.operation, tc.args)
		}
	}
}

// Every problem at once, so a caller fixes one call rather than finding the
// next fault on the next attempt.
func TestAllArgumentProblemsAreReportedTogether(t *testing.T) {
	p := newTestPlugin(t, &FakeBackend{}, nil)
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, "list_pods"),
		Arguments: map[string]any{"limit": "abc", "all_namespaces": "maybe"},
	})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "limit") || !strings.Contains(err.Error(), "all_namespaces") {
		t.Errorf("error names only some problems: %v", err)
	}
}

// An empty or absent numeric argument is not malformed — it means "use the
// default". The CLI cannot distinguish an unset flag from an empty one.
func TestEmptyNumericArgumentFallsBackToTheDefault(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)
	call(t, p, "list_pods", map[string]any{"limit": ""})
	if fake.LastPodQuery.Limit != DefaultListLimit {
		t.Errorf("limit = %d, want the default %d", fake.LastPodQuery.Limit, DefaultListLimit)
	}
}
