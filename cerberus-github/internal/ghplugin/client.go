package ghplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const defaultBaseURL = "https://api.github.com"

// requestTimeout bounds one GitHub call.
const requestTimeout = 30 * time.Second

// maxResponseBytes caps what one response may hand us. The largest answer
// these reads ask for is 100 workflow runs.
const maxResponseBytes = 16 << 20

// apiError is a response GitHub answered with a non-2xx status. The status is
// kept so the plugin can code it (a 401 is a credential problem, a 404 a
// repository that does not exist or that the token cannot see); the body is
// GitHub's own message.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

// Client is the GitHub REST API over net/http: the three reads the
// compiled-in connector made through go-github, without the SDK.
type Client struct {
	httpClient *http.Client
	token      string
	baseURL    string
}

var _ Backend = (*Client)(nil)

// NewClient creates a GitHub API client with the given token.
func NewClient(token string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: requestTimeout},
		token:      token,
		baseURL:    defaultBaseURL,
	}
}

// newBackend is the Backend constructor the plugin uses.
func newBackend(token string) Backend { return NewClient(token) }

// Wire shapes: GitHub's field names, decoded and then mapped onto the DTOs.
// They name only what the DTOs carry.

type wireRepo struct {
	Description   string    `json:"description"`
	DefaultBranch string    `json:"default_branch"`
	Private       bool      `json:"private"`
	Stargazers    int       `json:"stargazers_count"`
	OpenIssues    int       `json:"open_issues_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type wireRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
}

type wireRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HeadBranch string    `json:"head_branch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
	HTMLURL    string    `json:"html_url"`
}

func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func (c *Client) RepoStatus(ctx context.Context, owner, repo string) (*RepoStatus, error) {
	var r wireRepo
	if err := c.getJSON(ctx, repoPath(owner, repo), nil, &r); err != nil {
		return nil, fmt.Errorf("get repo %s/%s: %w", owner, repo, err)
	}
	return &RepoStatus{
		Owner:       owner,
		Repo:        repo,
		Description: r.Description,
		DefaultBr:   r.DefaultBranch,
		Private:     r.Private,
		Stars:       r.Stargazers,
		OpenIssues:  r.OpenIssues,
		UpdatedAt:   r.UpdatedAt,
	}, nil
}

func (c *Client) ListReleases(ctx context.Context, owner, repo string, limit int) ([]Release, error) {
	var releases []wireRelease
	if err := c.getJSON(ctx, repoPath(owner, repo)+"/releases", perPage(limit), &releases); err != nil {
		return nil, fmt.Errorf("list releases %s/%s: %w", owner, repo, err)
	}
	out := make([]Release, 0, len(releases))
	for _, r := range releases {
		out = append(out, Release(r))
	}
	return out, nil
}

func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo string, limit int) ([]WorkflowRun, error) {
	var resp struct {
		WorkflowRuns []wireRun `json:"workflow_runs"`
	}
	if err := c.getJSON(ctx, repoPath(owner, repo)+"/actions/runs", perPage(limit), &resp); err != nil {
		return nil, fmt.Errorf("list workflow runs %s/%s: %w", owner, repo, err)
	}
	out := make([]WorkflowRun, 0, len(resp.WorkflowRuns))
	for _, r := range resp.WorkflowRuns {
		out = append(out, WorkflowRun{
			ID:         r.ID,
			Name:       r.Name,
			Status:     r.Status,
			Conclusion: r.Conclusion,
			Branch:     r.HeadBranch,
			Event:      r.Event,
			CreatedAt:  r.CreatedAt,
			HTMLURL:    r.HTMLURL,
		})
	}
	return out, nil
}

func perPage(limit int) url.Values {
	return url.Values{"per_page": {strconv.Itoa(limit)}}
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &apiError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}
