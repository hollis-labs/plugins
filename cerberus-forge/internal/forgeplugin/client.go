package forgeplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://forge.laravel.com/api/v1"

// requestTimeout bounds one Forge call. The compiled-in connector had none, so
// a hung connection held the operation open indefinitely.
const requestTimeout = 30 * time.Second

// apiError is a response Forge answered with a non-2xx status. The status is
// kept so the plugin can code it (a 401 is a credential problem, a 404 a wrong
// id); the body is Forge's own message.
type apiError struct {
	StatusCode int
	Body       string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("API returned %d: %s", e.StatusCode, e.Body)
}

// Client is the Laravel Forge REST API over net/http, the same calls the
// compiled-in connector made.
type Client struct {
	httpClient *http.Client
	apiToken   string
	baseURL    string
}

var _ Backend = (*Client)(nil)

// NewClient creates a Forge API client with the given token.
func NewClient(apiToken string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: requestTimeout},
		apiToken:   apiToken,
		baseURL:    defaultBaseURL,
	}
}

// newBackend is the Backend constructor the plugin uses.
func newBackend(apiToken string) Backend { return NewClient(apiToken) }

func (c *Client) ListServers(ctx context.Context) ([]Server, error) {
	var resp struct {
		Servers []Server `json:"servers"`
	}
	if err := c.getJSON(ctx, "/servers", &resp); err != nil {
		return nil, fmt.Errorf("forge list servers: %w", err)
	}
	if resp.Servers == nil {
		resp.Servers = []Server{}
	}
	return resp.Servers, nil
}

func (c *Client) GetServer(ctx context.Context, serverID int) (*Server, error) {
	var resp struct {
		Server Server `json:"server"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/servers/%d", serverID), &resp); err != nil {
		return nil, fmt.Errorf("forge get server: %w", err)
	}
	return &resp.Server, nil
}

func (c *Client) ListSites(ctx context.Context, serverID int) ([]Site, error) {
	var resp struct {
		Sites []Site `json:"sites"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/servers/%d/sites", serverID), &resp); err != nil {
		return nil, fmt.Errorf("forge list sites: %w", err)
	}
	if resp.Sites == nil {
		resp.Sites = []Site{}
	}
	return resp.Sites, nil
}

// GetDeploymentScript returns the current deployment script. Forge answers
// with the script itself, not JSON.
func (c *Client) GetDeploymentScript(ctx context.Context, serverID, siteID int) (string, error) {
	body, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/servers/%d/sites/%d/deployment/script", serverID, siteID), nil)
	if err != nil {
		return "", fmt.Errorf("forge get deployment script: %w", err)
	}
	return string(body), nil
}

func (c *Client) UpdateDeploymentScript(ctx context.Context, serverID, siteID int, content string, autoSource bool) error {
	payload := map[string]any{"content": content, "auto_source": autoSource}
	if _, err := c.request(ctx, http.MethodPut, fmt.Sprintf("/servers/%d/sites/%d/deployment/script", serverID, siteID), payload); err != nil {
		return fmt.Errorf("forge update deployment script: %w", err)
	}
	return nil
}

func (c *Client) DeploySite(ctx context.Context, serverID, siteID int) error {
	if _, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/servers/%d/sites/%d/deployment/deploy", serverID, siteID), nil); err != nil {
		return fmt.Errorf("forge deploy site: %w", err)
	}
	return nil
}

func (c *Client) ExecuteSiteCommand(ctx context.Context, serverID, siteID int, command string) (*SiteCommand, error) {
	body, err := c.request(ctx, http.MethodPost, fmt.Sprintf("/servers/%d/sites/%d/commands", serverID, siteID), map[string]any{"command": command})
	if err != nil {
		return nil, fmt.Errorf("forge execute site command: %w", err)
	}
	var resp struct {
		Command SiteCommand `json:"command"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("forge execute site command: parse response: %w", err)
	}
	return &resp.Command, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}
