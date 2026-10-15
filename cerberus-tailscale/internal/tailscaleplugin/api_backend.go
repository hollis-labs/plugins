package tailscaleplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultAPIBase is the Tailscale control-plane API.
const DefaultAPIBase = "https://api.tailscale.com"

// maxListedKeys caps the per-key read-back list_auth_keys does (the list
// endpoint returns ids only).
const maxListedKeys = 100

// maxResponseBytes bounds what is read from the API.
const maxResponseBytes = 4 << 20

// Credentials is what the host resolved for this plugin. Either APIKey, or the
// OAuth client pair, is enough; APIKey wins when both are present.
type Credentials struct {
	APIKey            string
	OAuthClientID     string
	OAuthClientSecret string
}

// Present reports whether the credentials can authenticate at all.
func (c Credentials) Present() bool {
	return c.APIKey != "" || (c.OAuthClientID != "" && c.OAuthClientSecret != "")
}

// apiError is a non-2xx answer from the API. Message is the API's own text; it
// is scrubbed with the rest of an error before it leaves the plugin.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("Tailscale API answered %d", e.Status)
	if text := http.StatusText(e.Status); text != "" {
		msg += " " + text
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Status == http.StatusForbidden {
		msg += " (the credential lacks the scope, or does not own the tag, this call needs)"
	}
	return msg
}

// httpBackend talks to the Tailscale API over plain net/http. There is no
// vendor SDK; the wire shapes below are private and every DTO is built from
// them field by field.
type httpBackend struct {
	base   string
	creds  Credentials
	client *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// NewHTTPBackend builds the live backend. base is overridable for tests.
func NewHTTPBackend(base string, creds Credentials) Backend {
	if base == "" {
		base = DefaultAPIBase
	}
	return &httpBackend{
		base:   strings.TrimRight(base, "/"),
		creds:  creds,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// authorize sets the request's credential. An API key is sent as the basic-auth
// user with an empty password; an OAuth client is exchanged for a short-lived
// bearer token, cached until shortly before it expires.
func (b *httpBackend) authorize(ctx context.Context, req *http.Request) error {
	if b.creds.APIKey != "" {
		req.SetBasicAuth(b.creds.APIKey, "")
		return nil
	}
	token, err := b.oauthToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (b *httpBackend) oauthToken(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.accessToken != "" && time.Now().Before(b.tokenExpiry) {
		return b.accessToken, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {b.creds.OAuthClientID},
		"client_secret": {b.creds.OAuthClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+"/api/v2/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := b.roundTrip(req, &out); err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
			// An unknown client or a wrong secret is a rejected credential.
			apiErr.Status = http.StatusUnauthorized
		}
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("Tailscale OAuth exchange returned no access token")
	}
	lifetime := time.Duration(out.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 30 * time.Minute
	}
	b.accessToken = out.AccessToken
	b.tokenExpiry = time.Now().Add(lifetime - 30*time.Second)
	return b.accessToken, nil
}

// do sends an authorized JSON request. A nil body sends none; a nil out
// discards the response.
func (b *httpBackend) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := b.authorize(ctx, req); err != nil {
		return err
	}
	return b.roundTrip(req, out)
}

func (b *httpBackend) roundTrip(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{Status: resp.StatusCode, Message: apiMessage(data)}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode Tailscale API response: %w", err)
	}
	return nil
}

// apiMessage reads the API's error text, bounded. The API answers
// {"message": "..."}; anything else is dropped rather than echoed.
func apiMessage(data []byte) string {
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &body) != nil {
		return ""
	}
	const limit = 300
	if len(body.Message) > limit {
		return body.Message[:limit] + "..."
	}
	return body.Message
}

// --- wire shapes: private, decoded, then mapped field by field ---

type wireKey struct {
	ID           string `json:"id"`
	Key          string `json:"key"`
	KeyType      string `json:"keyType"`
	Description  string `json:"description"`
	Created      string `json:"created"`
	Expires      string `json:"expires"`
	Revoked      string `json:"revoked"`
	Invalid      bool   `json:"invalid"`
	Capabilities struct {
		Devices struct {
			Create struct {
				Reusable      bool     `json:"reusable"`
				Ephemeral     bool     `json:"ephemeral"`
				Preauthorized bool     `json:"preauthorized"`
				Tags          []string `json:"tags"`
			} `json:"create"`
		} `json:"devices"`
	} `json:"capabilities"`
}

type wireDevice struct {
	ID                string   `json:"id"`
	NodeID            string   `json:"nodeId"`
	Name              string   `json:"name"`
	Hostname          string   `json:"hostname"`
	User              string   `json:"user"`
	OS                string   `json:"os"`
	ClientVersion     string   `json:"clientVersion"`
	Addresses         []string `json:"addresses"`
	Tags              []string `json:"tags"`
	Authorized        bool     `json:"authorized"`
	KeyExpiryDisabled bool     `json:"keyExpiryDisabled"`
	UpdateAvailable   bool     `json:"updateAvailable"`
	IsExternal        bool     `json:"isExternal"`
	Created           string   `json:"created"`
	LastSeen          string   `json:"lastSeen"`
	Expires           string   `json:"expires"`
}

// parseTime reads an RFC 3339 time. The API reports "no expiry" as the zero
// time or an empty string; both are nil here.
func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Year() <= 1 {
		return nil
	}
	return &t
}

func authKeyFrom(w wireKey) AuthKey {
	create := w.Capabilities.Devices.Create
	return AuthKey{
		ID:            w.ID,
		KeyType:       w.KeyType,
		Description:   w.Description,
		Created:       parseTime(w.Created),
		Expires:       parseTime(w.Expires),
		Revoked:       parseTime(w.Revoked),
		Invalid:       w.Invalid,
		Reusable:      create.Reusable,
		Ephemeral:     create.Ephemeral,
		Preauthorized: create.Preauthorized,
		Tags:          append([]string(nil), create.Tags...),
	}
}

func deviceFrom(w wireDevice) Device {
	return Device{
		ID:                w.ID,
		NodeID:            w.NodeID,
		Name:              w.Name,
		Hostname:          w.Hostname,
		User:              w.User,
		OS:                w.OS,
		ClientVersion:     w.ClientVersion,
		Addresses:         append([]string(nil), w.Addresses...),
		Tags:              append([]string(nil), w.Tags...),
		Authorized:        w.Authorized,
		KeyExpiryDisabled: w.KeyExpiryDisabled,
		UpdateAvailable:   w.UpdateAvailable,
		IsExternal:        w.IsExternal,
		Created:           parseTime(w.Created),
		LastSeen:          parseTime(w.LastSeen),
		Expires:           parseTime(w.Expires),
	}
}

func tailnetPath(tailnet string) string {
	if tailnet == "" {
		tailnet = DefaultTailnet
	}
	return "/api/v2/tailnet/" + url.PathEscape(tailnet)
}

// --- Backend ---

func (b *httpBackend) ListAuthKeys(ctx context.Context, tailnet string) (AuthKeyList, error) {
	var listing struct {
		Keys []struct {
			ID string `json:"id"`
		} `json:"keys"`
	}
	if err := b.do(ctx, http.MethodGet, tailnetPath(tailnet)+"/keys", nil, &listing); err != nil {
		return AuthKeyList{}, err
	}
	out := AuthKeyList{Keys: []AuthKey{}}
	for i, entry := range listing.Keys {
		if i >= maxListedKeys {
			out.Truncated = true
			break
		}
		key, err := b.GetAuthKey(ctx, tailnet, entry.ID)
		if err != nil {
			return AuthKeyList{}, err
		}
		out.Keys = append(out.Keys, *key)
	}
	return out, nil
}

func (b *httpBackend) GetAuthKey(ctx context.Context, tailnet, id string) (*AuthKey, error) {
	var w wireKey
	if err := b.do(ctx, http.MethodGet, tailnetPath(tailnet)+"/keys/"+url.PathEscape(id), nil, &w); err != nil {
		return nil, err
	}
	key := authKeyFrom(w)
	return &key, nil
}

func (b *httpBackend) CreateAuthKey(ctx context.Context, tailnet string, req MintRequest) (*MintedKey, error) {
	body := map[string]any{
		"keyType":       "auth",
		"description":   req.Description,
		"expirySeconds": int64(req.TTL / time.Second),
		"capabilities": map[string]any{
			"devices": map[string]any{
				"create": map[string]any{
					"reusable":      req.Reusable,
					"ephemeral":     req.Ephemeral,
					"preauthorized": req.Preauthorized,
					"tags":          req.Tags,
				},
			},
		},
	}
	var w wireKey
	if err := b.do(ctx, http.MethodPost, tailnetPath(tailnet)+"/keys", body, &w); err != nil {
		return nil, err
	}
	if w.Key == "" {
		return nil, errors.New("Tailscale created the key but returned no key value; revoke it by id " + w.ID)
	}
	return &MintedKey{AuthKey: authKeyFrom(w), Key: w.Key, Note: mintNote}, nil
}

func (b *httpBackend) RevokeAuthKey(ctx context.Context, tailnet, id string) error {
	return b.do(ctx, http.MethodDelete, tailnetPath(tailnet)+"/keys/"+url.PathEscape(id), nil, nil)
}

func (b *httpBackend) ListDevices(ctx context.Context, tailnet string) (DeviceList, error) {
	var listing struct {
		Devices []wireDevice `json:"devices"`
	}
	if err := b.do(ctx, http.MethodGet, tailnetPath(tailnet)+"/devices", nil, &listing); err != nil {
		return DeviceList{}, err
	}
	out := DeviceList{Devices: []Device{}}
	for _, d := range listing.Devices {
		out.Devices = append(out.Devices, deviceFrom(d))
	}
	return out, nil
}

func (b *httpBackend) GetDevice(ctx context.Context, id string) (*Device, error) {
	var w wireDevice
	if err := b.do(ctx, http.MethodGet, "/api/v2/device/"+url.PathEscape(id), nil, &w); err != nil {
		return nil, err
	}
	device := deviceFrom(w)
	return &device, nil
}

func (b *httpBackend) DeleteDevice(ctx context.Context, id string) error {
	return b.do(ctx, http.MethodDelete, "/api/v2/device/"+url.PathEscape(id), nil, nil)
}

func (b *httpBackend) SetDeviceTags(ctx context.Context, id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	return b.do(ctx, http.MethodPost, "/api/v2/device/"+url.PathEscape(id)+"/tags", map[string]any{"tags": tags}, nil)
}

func (b *httpBackend) AuthorizeDevice(ctx context.Context, id string) error {
	return b.do(ctx, http.MethodPost, "/api/v2/device/"+url.PathEscape(id)+"/authorized", map[string]any{"authorized": true}, nil)
}
