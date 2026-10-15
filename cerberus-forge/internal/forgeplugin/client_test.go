package forgeplugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordedRequest struct {
	method, path, auth string
	body               map[string]any
}

func testClient(t *testing.T, status int, body string) (*Client, *recordedRequest) {
	t.Helper()
	seen := &recordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path, seen.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if data, _ := io.ReadAll(r.Body); len(data) > 0 {
			_ = json.Unmarshal(data, &seen.body)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	c := NewClient("forge-token-value")
	c.baseURL = server.URL
	return c, seen
}

// The client makes exactly the calls the compiled-in connector made.
func TestClientCallsTheForgeAPI(t *testing.T) {
	ctx := context.Background()

	c, seen := testClient(t, 200, fullSiteJSON)
	sites, err := c.ListSites(ctx, 12)
	if err != nil || len(sites) != 1 || seen.method != "GET" || seen.path != "/servers/12/sites" || seen.auth != "Bearer forge-token-value" {
		t.Fatalf("list sites: %v %+v %+v", err, sites, seen)
	}

	c, seen = testClient(t, 200, "cd /home/forge/site\ngit pull\n")
	script, err := c.GetDeploymentScript(ctx, 12, 34)
	if err != nil || script != "cd /home/forge/site\ngit pull\n" || seen.path != "/servers/12/sites/34/deployment/script" {
		t.Fatalf("script: %v %q %+v", err, script, seen)
	}

	c, seen = testClient(t, 200, "")
	if err := c.UpdateDeploymentScript(ctx, 12, 34, "git pull\n", true); err != nil || seen.method != "PUT" || seen.body["content"] != "git pull\n" || seen.body["auto_source"] != true {
		t.Fatalf("update: %v %+v", err, seen)
	}

	c, seen = testClient(t, 200, "")
	if err := c.DeploySite(ctx, 12, 34); err != nil || seen.method != "POST" || seen.path != "/servers/12/sites/34/deployment/deploy" {
		t.Fatalf("deploy: %v %+v", err, seen)
	}

	c, seen = testClient(t, 200, `{"command":{"id":9,"server_id":12,"site_id":34,"command":"true","status":"waiting"}}`)
	cmd, err := c.ExecuteSiteCommand(ctx, 12, 34, "true")
	if err != nil || cmd.ID != 9 || seen.path != "/servers/12/sites/34/commands" || seen.body["command"] != "true" {
		t.Fatalf("exec: %v %+v %+v", err, cmd, seen)
	}
}

// A non-2xx answer keeps its status, so the plugin can code it.
func TestClientKeepsTheStatus(t *testing.T) {
	c, _ := testClient(t, 401, `{"message":"Unauthenticated."}`)
	_, err := c.ListServers(context.Background())
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Fatalf("err = %v, want a 401 apiError", err)
	}
	if errorCode(err) != "credential_missing" {
		t.Fatalf("code = %q", errorCode(err))
	}
}
