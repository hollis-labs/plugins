package tailscaleplugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
)

// fakeTailscale is a fake control-plane API. Nothing here touches a real
// tailnet; every name is an example.
type fakeTailscale struct {
	mu       sync.Mutex
	requests []recorded
	handler  func(w http.ResponseWriter, r *http.Request, body []byte)
}

type recorded struct {
	Method, Path, Auth, ContentType string
	User                            string
	Body                            string
}

func newFakeTailscale(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*fakeTailscale, string) {
	t.Helper()
	f := &fakeTailscale{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		user, _, _ := r.BasicAuth()
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Content-Type"), user, string(body)})
		f.mu.Unlock()
		f.handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// A full wire key, with fields the DTO must not carry.
const wireKeyJSON = `{"id":"kExample1","keyType":"auth","description":"ci runner","created":"2026-10-01T00:00:00Z",
"expires":"2026-10-01T01:00:00Z","revoked":"0001-01-01T00:00:00Z","invalid":false,"userId":"u123","somethingNew":"x",
"capabilities":{"devices":{"create":{"reusable":false,"ephemeral":true,"preauthorized":true,"tags":["tag:ci"]}}}}`

func TestAPIKeyIsSentAsBasicAuthAndKeysMapToDTOs(t *testing.T) {
	fake, base := newFakeTailscale(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/api/v2/tailnet/example.com/keys":
			writeJSON(w, 200, `{"keys":[{"id":"kExample1"}]}`)
		case "/api/v2/tailnet/example.com/keys/kExample1":
			writeJSON(w, 200, wireKeyJSON)
		default:
			writeJSON(w, 404, `{"message":"not found"}`)
		}
	})
	b := NewHTTPBackend(base, Credentials{APIKey: "api-key-example"})
	list, err := b.ListAuthKeys(context.Background(), "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != 1 || list.Truncated {
		t.Fatalf("list = %+v", list)
	}
	key := list.Keys[0]
	if key.ID != "kExample1" || key.KeyType != "auth" || !key.Ephemeral || !key.Preauthorized || key.Reusable ||
		len(key.Tags) != 1 || key.Expires == nil || key.Revoked != nil {
		t.Errorf("key = %+v", key)
	}
	// The DTO is an allow-list: nothing the API added survives encoding.
	encoded, _ := json.Marshal(list)
	for _, leak := range []string{"u123", "somethingNew", "userId"} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("DTO carries %q: %s", leak, encoded)
		}
	}
	for _, r := range fake.requests {
		if r.User != "api-key-example" || r.Auth != "" && !strings.HasPrefix(r.Auth, "Basic ") {
			t.Errorf("request auth = %+v", r)
		}
	}
}

func TestOAuthClientIsExchangedOnceAndSentAsBearer(t *testing.T) {
	fake, base := newFakeTailscale(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path == "/api/v2/oauth/token" {
			if !strings.Contains(string(body), "grant_type=client_credentials") {
				t.Errorf("token body = %s", body)
			}
			writeJSON(w, 200, `{"access_token":"access-token-example","expires_in":3600}`)
			return
		}
		writeJSON(w, 200, `{"devices":[{"id":"n1","hostname":"host-1","addresses":["100.64.0.1"],"authorized":true,"lastSeen":"2026-10-10T00:00:00Z","expires":"0001-01-01T00:00:00Z","routes":["10.0.0.0/8"]}]}`)
	})
	b := NewHTTPBackend(base, Credentials{OAuthClientID: "client-id-example", OAuthClientSecret: "client-secret-example"})
	for i := 0; i < 2; i++ {
		devices, err := b.ListDevices(context.Background(), "-")
		if err != nil {
			t.Fatal(err)
		}
		d := devices.Devices[0]
		if d.ID != "n1" || d.Hostname != "host-1" || !d.Authorized || d.LastSeen == nil || d.Expires != nil {
			t.Errorf("device = %+v", d)
		}
		if encoded, _ := json.Marshal(devices); strings.Contains(string(encoded), "10.0.0.0") {
			t.Errorf("DTO carries routes: %s", encoded)
		}
	}
	tokenCalls := 0
	for _, r := range fake.requests {
		if r.Path == "/api/v2/oauth/token" {
			tokenCalls++
			continue
		}
		if r.Auth != "Bearer access-token-example" {
			t.Errorf("auth = %q", r.Auth)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("token exchanged %d times, want once (cached)", tokenCalls)
	}
}

func TestMintSendsTheRequestedCapabilitiesAndReturnsTheKeyOnce(t *testing.T) {
	fake, base := newFakeTailscale(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/tailnet/-/keys" {
			writeJSON(w, 200, `{"id":"kNew1","key":"`+mintedValue+`","keyType":"auth","created":"2026-10-01T00:00:00Z","expires":"2026-10-01T02:00:00Z",
"capabilities":{"devices":{"create":{"reusable":false,"ephemeral":true,"preauthorized":true,"tags":["tag:ci"]}}}}`)
			return
		}
		writeJSON(w, 200, wireKeyJSON) // a later read: no key value
	})
	b := NewHTTPBackend(base, Credentials{APIKey: "api-key-example"})
	minted, err := b.CreateAuthKey(context.Background(), "", MintRequest{
		Description: "ci", Tags: []string{"tag:ci"}, Ephemeral: true, Preauthorized: true, TTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if minted.Key != mintedValue || minted.ID != "kNew1" {
		t.Errorf("minted = %+v", minted)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(fake.requests[0].Body), &sent); err != nil {
		t.Fatal(err)
	}
	create := sent["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	if sent["keyType"] != "auth" || sent["expirySeconds"] != float64(7200) || create["ephemeral"] != true ||
		create["reusable"] != false || create["preauthorized"] != true || sent["description"] != "ci" {
		t.Errorf("sent = %s", fake.requests[0].Body)
	}
	if fake.requests[0].ContentType != "application/json" {
		t.Errorf("content type = %q", fake.requests[0].ContentType)
	}
	// Reading the key back never returns the value.
	got, err := b.GetAuthKey(context.Background(), "-", "kExample1")
	if err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(got); strings.Contains(string(encoded), "tskey") {
		t.Errorf("a read-back carries a key value: %s", encoded)
	}
}

func TestDeviceWritesHitTheDocumentedEndpoints(t *testing.T) {
	fake, base := newFakeTailscale(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeJSON(w, 200, `{}`) })
	b := NewHTTPBackend(base, Credentials{APIKey: "api-key-example"})
	ctx := context.Background()
	for _, err := range []error{
		b.RevokeAuthKey(ctx, "example.com", "kExample1"),
		b.DeleteDevice(ctx, "n1"),
		b.SetDeviceTags(ctx, "n1", []string{"tag:a"}),
		b.SetDeviceTags(ctx, "n1", nil),
		b.AuthorizeDevice(ctx, "n1"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"DELETE /api/v2/tailnet/example.com/keys/kExample1",
		"DELETE /api/v2/device/n1",
		"POST /api/v2/device/n1/tags " + `{"tags":["tag:a"]}`,
		"POST /api/v2/device/n1/tags " + `{"tags":[]}`,
		"POST /api/v2/device/n1/authorized " + `{"authorized":true}`,
	}
	for i, r := range fake.requests {
		got := r.Method + " " + r.Path
		if r.Body != "" {
			got += " " + r.Body
		}
		if got != want[i] {
			t.Errorf("request %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestListIsCappedAndSaysSo(t *testing.T) {
	_, base := newFakeTailscale(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if strings.HasSuffix(r.URL.Path, "/keys") {
			var ids []string
			for i := 0; i < maxListedKeys+5; i++ {
				ids = append(ids, `{"id":"k`+string(rune('a'+i%26))+`"}`)
			}
			writeJSON(w, 200, `{"keys":[`+strings.Join(ids, ",")+`]}`)
			return
		}
		writeJSON(w, 200, wireKeyJSON)
	})
	list, err := NewHTTPBackend(base, Credentials{APIKey: "api-key-example"}).ListAuthKeys(context.Background(), "-")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Keys) != maxListedKeys || !list.Truncated {
		t.Errorf("keys %d truncated %v", len(list.Keys), list.Truncated)
	}
}

// Through the whole plugin, against the fake server: statuses become the codes
// a caller branches on, and a credential the API echoes back is scrubbed.
func TestStatusesBecomeCodesAndEchoedCredentialsAreScrubbed(t *testing.T) {
	status := 401
	echo := ""
	_, base := newFakeTailscale(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, status, `{"message":"bad credential `+echo+`"}`)
	})
	p := NewWithBackend(NewHTTPBackend(base, Credentials{APIKey: "api-key-example-value"}))
	p.scrub = newScrubber("api-key-example-value")

	for _, tc := range []struct {
		status int
		want   cerbplugin.ErrorCode
	}{
		{401, cerbplugin.ErrorCredentialMissing},
		{404, cerbplugin.ErrorInvalidArgs},
		{400, cerbplugin.ErrorInvalidArgs},
		{403, ""},
	} {
		status, echo = tc.status, "api-key-example-value"
		result, err := callTool(p, "list_devices", nil)
		code, message := failure(t, result, err)
		if code != tc.want {
			t.Errorf("%d: code = %q, want %q", tc.status, code, tc.want)
		}
		if strings.Contains(message, "api-key-example-value") {
			t.Errorf("%d: message carries the credential: %s", tc.status, message)
		}
		if tc.status == 403 && !strings.Contains(message, "scope") {
			t.Errorf("403 message does not point at the scope: %s", message)
		}
	}
}

// A read-only credential's 403 on a write surfaces as a failure and the
// plugin does not retry or pretend: the live check relies on this.
func TestRefusedWriteIsAnErrorNotASuccess(t *testing.T) {
	_, base := newFakeTailscale(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodGet {
			writeJSON(w, 403, `{"message":"calling actor does not have enough permissions"}`)
			return
		}
		writeJSON(w, 200, `{"id":"n1","hostname":"host-1"}`)
	})
	p := NewWithBackend(NewHTTPBackend(base, Credentials{APIKey: "read-only-key-example"}))
	for _, op := range []string{"delete_device", "authorize_device", "set_tags", "mint_auth_key", "revoke_key"} {
		result, err := callTool(p, op, validArgs(op))
		if err == nil && !result.IsError {
			t.Errorf("%s succeeded against a credential the API refuses", op)
		}
	}
}

func TestUnreachableAPIIsUnavailable(t *testing.T) {
	_, base := newFakeTailscale(t, func(http.ResponseWriter, *http.Request, []byte) {})
	// Closed server: a refused connection.
	b := NewHTTPBackend("http://127.0.0.1:1", Credentials{APIKey: "api-key-example"})
	_ = base
	result, err := callTool(NewWithBackend(b), "list_devices", nil)
	if code, _ := failure(t, result, err); code != cerbplugin.ErrorUnavailable {
		t.Errorf("code = %q, want connector_unavailable", code)
	}
}
