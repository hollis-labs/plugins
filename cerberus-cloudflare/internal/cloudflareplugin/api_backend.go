package cloudflareplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultAPIBase is the Cloudflare API.
const DefaultAPIBase = "https://api.cloudflare.com/client/v4"

// maxResponseBytes bounds what is read from the API.
const maxResponseBytes = 4 << 20

// apiBackend talks to the Cloudflare API over plain net/http, for the calls
// the plugin makes that cloudflare-go does not cover well (token verification,
// R2, token minting, a DNS update by name). The wire shapes below are private
// to this file and every DTO is built from them field by field, so this file
// and sdk_backend.go together are the whole vendor boundary.
type apiBackend struct {
	base   string
	token  string
	client *http.Client
}

// newAPIBackend builds the HTTP half of the live backend. base is overridable
// for tests.
func newAPIBackend(base, token string) *apiBackend {
	if base == "" {
		base = DefaultAPIBase
	}
	return &apiBackend{
		base:   strings.TrimRight(base, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// apiError is a non-2xx answer, or a 2xx whose envelope says success is false.
// Message is Cloudflare's own text; it is scrubbed with the rest of an error
// before it leaves the plugin.
type apiError struct {
	Status  int
	Codes   []int
	Message string
}

func (e *apiError) Error() string {
	msg := fmt.Sprintf("Cloudflare API answered %d", e.Status)
	if text := http.StatusText(e.Status); text != "" {
		msg += " " + text
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Status == http.StatusForbidden {
		msg += " (the token lacks the permission this call needs)"
	}
	return msg
}

// envelope is Cloudflare's response wrapper.
type envelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// do sends an authorized JSON request. A nil body sends none; a nil out
// discards the result.
func (b *apiBackend) do(ctx context.Context, method, path string, query url.Values, header map[string]string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	target := b.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	var env envelope
	decodeErr := json.Unmarshal(data, &env)
	if resp.StatusCode < 200 || resp.StatusCode > 299 || (decodeErr == nil && !env.Success) {
		status := resp.StatusCode
		if status >= 200 && status <= 299 {
			status = http.StatusBadGateway
		}
		return apiErrorFrom(status, env)
	}
	if decodeErr != nil {
		return fmt.Errorf("decode Cloudflare API response: %w", decodeErr)
	}
	if out == nil {
		return nil
	}
	trimmed := bytes.TrimSpace(env.Result)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode Cloudflare API result: %w", err)
	}
	return nil
}

func apiErrorFrom(status int, env envelope) *apiError {
	e := &apiError{Status: status}
	var parts []string
	for _, item := range env.Errors {
		e.Codes = append(e.Codes, item.Code)
		if item.Message != "" {
			parts = append(parts, item.Message)
		}
	}
	const limit = 300
	e.Message = strings.Join(parts, "; ")
	if len(e.Message) > limit {
		e.Message = e.Message[:limit] + "..."
	}
	return e
}

// wireToken is the token-verify and token-read result. Only the fields named
// here are read.
type wireToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	ExpiresOn string `json:"expires_on"`
	NotBefore string `json:"not_before"`
	Policies  []struct {
		Effect           string         `json:"effect"`
		Resources        map[string]any `json:"resources"`
		PermissionGroups []struct {
			Name string `json:"name"`
		} `json:"permission_groups"`
	} `json:"policies"`
}

// tokenBase is the token API root: account-owned tokens live under the account,
// user-owned tokens under /user.
func tokenBase(accountID string) string {
	if accountID != "" {
		return "/accounts/" + url.PathEscape(accountID) + "/tokens"
	}
	return "/user/tokens"
}

func (b *apiBackend) VerifyToken(ctx context.Context, accountID string) (*TokenStatus, error) {
	var verified wireToken
	if err := b.do(ctx, http.MethodGet, tokenBase(accountID)+"/verify", nil, nil, nil, &verified); err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}
	scope := "user"
	if accountID != "" {
		scope = "account"
	}
	out := &TokenStatus{
		ID:        verified.ID,
		Status:    verified.Status,
		Scope:     scope,
		ExpiresOn: verified.ExpiresOn,
		NotBefore: verified.NotBefore,
		Policies:  []TokenPolicy{},
	}

	// The permission groups come from reading the token itself, which needs a
	// permission the token does not necessarily hold. A refusal here is not a
	// failed verification: report the gap instead.
	var detail wireToken
	err := b.do(ctx, http.MethodGet, tokenBase(accountID)+"/"+url.PathEscape(verified.ID), nil, nil, nil, &detail)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) {
			out.Note = "the token verified, but the permission groups it holds could not be read: reading a token needs an API Tokens read permission on it"
			return out, nil
		}
		return nil, fmt.Errorf("read token: %w", err)
	}
	out.PoliciesAvailable = true
	out.Policies = policiesFromWire(detail)
	return out, nil
}

// policiesFromWire names every field that leaves: the effect, the permission
// group names and the resource keys. Group ids and request conditions (IP
// allow-lists) are not copied.
func policiesFromWire(t wireToken) []TokenPolicy {
	out := make([]TokenPolicy, 0, len(t.Policies))
	for _, p := range t.Policies {
		policy := TokenPolicy{Effect: p.Effect, PermissionGroups: []string{}, Resources: []string{}}
		for _, g := range p.PermissionGroups {
			policy.PermissionGroups = append(policy.PermissionGroups, g.Name)
		}
		for key, value := range p.Resources {
			if s, ok := value.(string); ok && s != "" {
				policy.Resources = append(policy.Resources, key+" = "+s)
			} else {
				policy.Resources = append(policy.Resources, key)
			}
		}
		sort.Strings(policy.Resources)
		out = append(out, policy)
	}
	return out
}

// wireBucket is an R2 bucket as the API returns it.
type wireBucket struct {
	Name         string `json:"name"`
	CreationDate string `json:"creation_date"`
	Location     string `json:"location"`
	StorageClass string `json:"storage_class"`
	Jurisdiction string `json:"jurisdiction"`
}

// bucketFromWire names every field that leaves.
func bucketFromWire(w wireBucket) Bucket {
	return Bucket{
		Name:         w.Name,
		CreatedAt:    w.CreationDate,
		Location:     w.Location,
		StorageClass: w.StorageClass,
		Jurisdiction: w.Jurisdiction,
	}
}

func r2Path(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/r2/buckets"
}

// jurisdictionHeader selects a non-default R2 jurisdiction. The default one is
// addressed by sending nothing.
func jurisdictionHeader(jurisdiction string) map[string]string {
	if jurisdiction == "" || jurisdiction == "default" {
		return nil
	}
	return map[string]string{"cf-r2-jurisdiction": jurisdiction}
}

func (b *apiBackend) ListBuckets(ctx context.Context, accountID, jurisdiction string) ([]Bucket, error) {
	var out struct {
		Buckets []wireBucket `json:"buckets"`
	}
	query := url.Values{"per_page": {"1000"}}
	if err := b.do(ctx, http.MethodGet, r2Path(accountID), query, jurisdictionHeader(jurisdiction), nil, &out); err != nil {
		return nil, fmt.Errorf("list r2 buckets in account %s: %w", accountID, err)
	}
	buckets := make([]Bucket, 0, len(out.Buckets))
	for _, w := range out.Buckets {
		buckets = append(buckets, bucketFromWire(w))
	}
	return buckets, nil
}

func (b *apiBackend) CreateBucket(ctx context.Context, accountID string, req BucketRequest) (*Bucket, error) {
	body := map[string]any{"name": req.Name}
	if req.LocationHint != "" {
		body["locationHint"] = req.LocationHint
	}
	if req.StorageClass != "" {
		body["storageClass"] = req.StorageClass
	}
	var out wireBucket
	if err := b.do(ctx, http.MethodPost, r2Path(accountID), nil, jurisdictionHeader(req.Jurisdiction), body, &out); err != nil {
		return nil, fmt.Errorf("create r2 bucket %s in account %s: %w", req.Name, accountID, err)
	}
	bucket := bucketFromWire(out)
	return &bucket, nil
}

func (b *apiBackend) DeleteBucket(ctx context.Context, accountID, name, jurisdiction string) error {
	if err := b.do(ctx, http.MethodDelete, r2Path(accountID)+"/"+url.PathEscape(name), nil, jurisdictionHeader(jurisdiction), nil, nil); err != nil {
		return fmt.Errorf("delete r2 bucket %s in account %s: %w", name, accountID, err)
	}
	return nil
}
