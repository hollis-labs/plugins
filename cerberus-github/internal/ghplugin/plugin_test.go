package ghplugin

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

type call struct {
	method string
	args   []any
}

type fakeBackend struct {
	err   error
	calls []call
}

func (f *fakeBackend) record(method string, args ...any) {
	f.calls = append(f.calls, call{method, args})
}

func (f *fakeBackend) RepoStatus(_ context.Context, owner, repo string) (*RepoStatus, error) {
	f.record("RepoStatus", owner, repo)
	if f.err != nil {
		return nil, f.err
	}
	return &RepoStatus{Owner: owner, Repo: repo, DefaultBr: "main"}, nil
}

func (f *fakeBackend) ListReleases(_ context.Context, owner, repo string, limit int) ([]Release, error) {
	f.record("ListReleases", owner, repo, limit)
	if f.err != nil {
		return nil, f.err
	}
	return []Release{{TagName: "v1"}}, nil
}

func (f *fakeBackend) ListWorkflowRuns(_ context.Context, owner, repo string, limit int) ([]WorkflowRun, error) {
	f.record("ListWorkflowRuns", owner, repo, limit)
	if f.err != nil {
		return nil, f.err
	}
	return []WorkflowRun{{ID: 9007199254740993, Name: "ci"}}, nil
}

func loadedPlugin(t *testing.T, backend Backend) *Plugin {
	t.Helper()
	p := NewWithBackend(backend)
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

func callTool(p *Plugin, operation string, args map[string]any) (subprocess.MCPCallResult, error) {
	return p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
}

func mustCall(t *testing.T, p *Plugin, operation string, args map[string]any) subprocess.MCPCallResult {
	t.Helper()
	result, err := callTool(p, operation, args)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if result.IsError {
		t.Fatalf("%s: %s", operation, result.Content)
	}
	return result
}

// failure reads a failed call either way it can come back: coded, as an error
// result the host maps onto its own code, or as a plain error.
func failure(t *testing.T, result subprocess.MCPCallResult, err error) (cerbplugin.ErrorCode, string) {
	t.Helper()
	if err != nil {
		return "", err.Error()
	}
	code, message, ok := cerbplugin.ParseErrorResult(result.Content)
	if !result.IsError || !ok {
		t.Fatalf("call succeeded (%s), want a failure", result.Content)
	}
	return code, message
}

func validArgs(string) map[string]any {
	return map[string]any{"owner": "hollis-labs", "repo": "cerberus"}
}

// Each operation reaches the backend call the compiled-in connector made,
// with the repository and limit it was given.
func TestOperationsCallTheBackend(t *testing.T) {
	cases := []struct {
		op   string
		args map[string]any
		want call
	}{
		{"status", validArgs(""), call{"RepoStatus", []any{"hollis-labs", "cerberus"}}},
		{"list_releases", validArgs(""), call{"ListReleases", []any{"hollis-labs", "cerberus", 10}}},
		{"list_releases", map[string]any{"owner": "o", "repo": "r.go", "limit": float64(5)}, call{"ListReleases", []any{"o", "r.go", 5}}},
		{"list_workflow_runs", map[string]any{"owner": "o", "repo": "r", "limit": "100"}, call{"ListWorkflowRuns", []any{"o", "r", 100}}},
	}
	for _, tc := range cases {
		backend := &fakeBackend{}
		mustCall(t, loadedPlugin(t, backend), tc.op, tc.args)
		if len(backend.calls) != 1 || !reflect.DeepEqual(backend.calls[0], tc.want) {
			t.Errorf("%s: calls = %+v, want %+v", tc.op, backend.calls, tc.want)
		}
	}
}

// The result is the DTO as JSON; a workflow run id beyond float64 precision
// survives, as it did from the built-in.
func TestResultsAreTheDTOs(t *testing.T) {
	result := mustCall(t, loadedPlugin(t, &fakeBackend{}), "list_workflow_runs", validArgs(""))
	if !strings.Contains(string(result.Content), `"id":9007199254740993`) {
		t.Fatalf("content = %s", result.Content)
	}
	var runs []WorkflowRun
	if err := json.Unmarshal(result.Content, &runs); err != nil || len(runs) != 1 {
		t.Fatalf("decode: %v %s", err, result.Content)
	}
}

// Arguments that cannot name a repository, or a limit GitHub would not
// honour, are refused before any call, all at once, as invalid_args.
func TestBadArgumentsAreRefusedBeforeACall(t *testing.T) {
	cases := map[string]map[string]any{
		"missing both":    {},
		"path traversal":  {"owner": "hollis-labs", "repo": ".."},
		"slash in repo":   {"owner": "hollis-labs", "repo": "cerberus/../../user"},
		"query in owner":  {"owner": "o?per_page=1", "repo": "r"},
		"limit zero":      {"owner": "o", "repo": "r", "limit": float64(0)},
		"limit too large": {"owner": "o", "repo": "r", "limit": "101"},
		"limit fraction":  {"owner": "o", "repo": "r", "limit": 2.5},
		"limit word":      {"owner": "o", "repo": "r", "limit": "ten"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			backend := &fakeBackend{}
			result, err := callTool(loadedPlugin(t, backend), "list_releases", args)
			code, message := failure(t, result, err)
			if code != cerbplugin.ErrorInvalidArgs {
				t.Fatalf("code = %q (%s), want invalid_args", code, message)
			}
			if len(backend.calls) != 0 {
				t.Fatalf("backend was called: %+v", backend.calls)
			}
		})
	}
	result, err := callTool(loadedPlugin(t, &fakeBackend{}), "status", map[string]any{})
	_, message := failure(t, result, err)
	if !strings.Contains(message, "owner is required") || !strings.Contains(message, "repo is required") {
		t.Fatalf("want both problems in one message: %s", message)
	}
}

func TestDryRunIsRefused(t *testing.T) {
	for _, op := range Definition().Operations {
		args := validArgs("")
		args[argDryRun] = true
		result, err := callTool(loadedPlugin(t, &fakeBackend{}), op.Name, args)
		if code, message := failure(t, result, err); code != cerbplugin.ErrorInvalidArgs {
			t.Errorf("%s: code = %q (%s)", op.Name, code, message)
		}
	}
}

// With no token the plugin loads, reports the gap in Health, and every call
// fails as credential_missing.
func TestMissingTokenIsCredentialMissing(t *testing.T) {
	t.Setenv(TokenEnvVar, "")
	p := New()
	if _, err := p.Init(context.Background(), subprocess.InitParams{}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatalf("Load must not fail on a missing token: %v", err)
	}
	status, _ := p.Health(context.Background())
	if status.OK || !strings.Contains(status.Message, "github/token") {
		t.Fatalf("health = %+v", status)
	}
	for _, op := range Definition().Operations {
		result, err := callTool(p, op.Name, validArgs(""))
		if code, message := failure(t, result, err); code != cerbplugin.ErrorCredentialMissing {
			t.Errorf("%s: code = %q (%s)", op.Name, code, message)
		}
	}
}

// The token arrives from the host in init config and builds the backend.
func TestTokenFromInitConfig(t *testing.T) {
	var got string
	p := &Plugin{newBackend: func(token string) Backend { got = token; return &fakeBackend{} }}
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: map[string]string{SecretToken: "from-host"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "from-host" {
		t.Fatalf("backend built with %q", got)
	}
	if status, _ := p.Health(context.Background()); !status.OK {
		t.Fatalf("health = %+v", status)
	}
}

func TestUnknownToolIsRefused(t *testing.T) {
	p := loadedPlugin(t, &fakeBackend{})
	if _, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: "cerberus_github_create_release"}); err == nil {
		t.Fatal("an undeclared operation was served")
	}
}
