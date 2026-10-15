package ghplugin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type recordedRequest struct {
	method, path, query, auth, accept, version string
}

func testClient(t *testing.T, status int, body string) (*Client, *recordedRequest) {
	t.Helper()
	seen := &recordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path, seen.query = r.Method, r.URL.EscapedPath(), r.URL.RawQuery
		seen.auth, seen.accept, seen.version = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.Header.Get("X-GitHub-Api-Version")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c := NewClient("github-token-value")
	c.baseURL = server.URL
	return c, seen
}

// The client makes the three REST calls the compiled-in connector made
// through go-github, and maps each response onto the built-in's field names.
func TestClientCallsTheGitHubAPI(t *testing.T) {
	ctx := context.Background()

	c, seen := testClient(t, 200, fullRepoJSON)
	status, err := c.RepoStatus(ctx, "hollis-labs", "cerberus")
	if err != nil || seen.method != "GET" || seen.path != "/repos/hollis-labs/cerberus" ||
		seen.auth != "Bearer github-token-value" || seen.accept != "application/vnd.github+json" || seen.version != "2022-11-28" {
		t.Fatalf("status: %v %+v", err, seen)
	}
	want := RepoStatus{Owner: "hollis-labs", Repo: "cerberus", Description: "control plane", DefaultBr: "main",
		Private: false, Stars: 7, OpenIssues: 3, UpdatedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	if *status != want {
		t.Fatalf("status = %+v, want %+v", *status, want)
	}

	c, seen = testClient(t, 200, fullReleasesJSON)
	releases, err := c.ListReleases(ctx, "hollis-labs", "cerberus", 5)
	if err != nil || seen.path != "/repos/hollis-labs/cerberus/releases" || seen.query != "per_page=5" {
		t.Fatalf("releases: %v %+v", err, seen)
	}
	if len(releases) != 1 || releases[0].TagName != "v0.5.0-beta.1" || !releases[0].Prerelease || releases[0].HTMLURL == "" {
		t.Fatalf("releases = %+v", releases)
	}

	c, seen = testClient(t, 200, fullRunsJSON)
	runs, err := c.ListWorkflowRuns(ctx, "hollis-labs", "cerberus", 10)
	if err != nil || seen.path != "/repos/hollis-labs/cerberus/actions/runs" || seen.query != "per_page=10" {
		t.Fatalf("runs: %v %+v", err, seen)
	}
	if len(runs) != 1 || runs[0].ID != 9007199254740993 || runs[0].Branch != "main" || runs[0].Conclusion != "success" {
		t.Fatalf("runs = %+v", runs)
	}

	// An empty list is an empty array, never null.
	c, _ = testClient(t, 200, `[]`)
	if releases, err := c.ListReleases(ctx, "o", "r", 1); err != nil || releases == nil {
		t.Fatalf("empty releases: %v %#v", err, releases)
	}
}

// A non-2xx answer keeps its status, so the plugin can code it.
func TestClientKeepsTheStatus(t *testing.T) {
	c, _ := testClient(t, 401, `{"message":"Bad credentials"}`)
	_, err := c.RepoStatus(context.Background(), "o", "r")
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("err = %v, want a 401 apiError", err)
	}
	if errorCode(err) != "credential_missing" {
		t.Fatalf("code = %q", errorCode(err))
	}
}
