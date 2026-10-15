package ghplugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Recorded-shape GitHub responses carrying fields the DTOs do not name: owner
// and actor objects with their node ids and avatar URLs, a repository's
// permissions and clone URLs, a release's uploader and asset download URLs, a
// run's head commit with author email. Mapping onto the DTO is the allow-list
// (ADR 0003): none of it may reach CLI output, MCP results or an agent's
// context.
const fullRepoJSON = `{"id":1,"node_id":"SENTINEL-NODE","name":"cerberus","full_name":"hollis-labs/cerberus",
"private":false,"owner":{"login":"hollis-labs","id":2,"node_id":"SENTINEL-OWNER-NODE","avatar_url":"https://avatars.example.invalid/SENTINEL"},
"description":"control plane","fork":false,"clone_url":"https://SENTINEL-CLONE@github.com/hollis-labs/cerberus.git",
"ssh_url":"git@github.com:hollis-labs/cerberus.git","stargazers_count":7,"watchers_count":7,"open_issues_count":3,
"default_branch":"main","permissions":{"admin":true,"push":true,"pull":true},"security_and_analysis":{"secret_scanning":{"status":"enabled"}},
"updated_at":"2026-09-30T12:00:00Z","pushed_at":"2026-09-30T12:00:00Z"}`

const fullReleasesJSON = `[{"id":3,"node_id":"SENTINEL-RELEASE-NODE","tag_name":"v0.5.0-beta.1","target_commitish":"main",
"name":"v0.5.0-beta.1","draft":false,"prerelease":true,"created_at":"2026-09-29T00:00:00Z","published_at":"2026-09-29T00:00:00Z",
"author":{"login":"SENTINEL-AUTHOR","id":4},"body":"SENTINEL release notes","html_url":"https://github.com/hollis-labs/cerberus/releases/tag/v0.5.0-beta.1",
"upload_url":"https://uploads.github.com/SENTINEL-UPLOAD{?name,label}","tarball_url":"https://api.github.com/SENTINEL-TARBALL",
"assets":[{"name":"cerberus.tar.gz","browser_download_url":"https://github.com/SENTINEL-DOWNLOAD","uploader":{"login":"SENTINEL-UPLOADER"}}]}]`

const fullRunsJSON = `{"total_count":1,"workflow_runs":[{"id":9007199254740993,"name":"ci","node_id":"SENTINEL-RUN-NODE",
"head_branch":"main","head_sha":"SENTINEL-SHA","path":".github/workflows/ci.yml","run_number":12,"event":"push",
"status":"completed","conclusion":"success","workflow_id":5,"html_url":"https://github.com/hollis-labs/cerberus/actions/runs/9007199254740993",
"created_at":"2026-09-30T12:00:00Z","actor":{"login":"SENTINEL-ACTOR"},"triggering_actor":{"login":"SENTINEL-TRIGGER"},
"head_commit":{"id":"SENTINEL-SHA","message":"SENTINEL commit message","author":{"name":"SENTINEL","email":"sentinel@example.invalid"}},
"jobs_url":"https://api.github.com/SENTINEL-JOBS","logs_url":"https://api.github.com/SENTINEL-LOGS"}]}`

func TestDTOsOmitEverythingNotNamed(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, body, want string
		read             func(*Client) (any, error)
	}{
		{"repo", fullRepoJSON,
			`{"owner":"hollis-labs","repo":"cerberus","description":"control plane","default_branch":"main","private":false,"stars":7,"open_issues":3,"updated_at":"2026-09-30T12:00:00Z"}`,
			func(c *Client) (any, error) { return c.RepoStatus(ctx, "hollis-labs", "cerberus") }},
		{"releases", fullReleasesJSON,
			`[{"tag_name":"v0.5.0-beta.1","name":"v0.5.0-beta.1","draft":false,"prerelease":true,"published_at":"2026-09-29T00:00:00Z","html_url":"https://github.com/hollis-labs/cerberus/releases/tag/v0.5.0-beta.1"}]`,
			func(c *Client) (any, error) { return c.ListReleases(ctx, "hollis-labs", "cerberus", 10) }},
		{"runs", fullRunsJSON,
			`[{"id":9007199254740993,"name":"ci","status":"completed","conclusion":"success","branch":"main","event":"push","created_at":"2026-09-30T12:00:00Z","html_url":"https://github.com/hollis-labs/cerberus/actions/runs/9007199254740993"}]`,
			func(c *Client) (any, error) { return c.ListWorkflowRuns(ctx, "hollis-labs", "cerberus", 10) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, 200, tc.body)
			value, err := tc.read(c)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(value)
			got := string(encoded)
			if strings.Contains(got, "SENTINEL") {
				t.Fatalf("serialized %s leaked an unnamed field:\n%s", tc.name, got)
			}
			if got != tc.want {
				t.Fatalf("%s =\n %s\nwant (the built-in's field names)\n %s", tc.name, got, tc.want)
			}
		})
	}
}
