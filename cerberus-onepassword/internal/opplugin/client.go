package opplugin

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	onepassword "github.com/1password/onepassword-sdk-go"
)

// resolver is the one thing this plugin asks of 1Password: the value a
// reference names.
type resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// tokenPrefix is how every 1Password service account token begins.
const tokenPrefix = "ops_"

// errNotAServiceAccountToken is the refusal for anything else in
// service_account_token. It names what is wrong, never the value.
var errNotAServiceAccountToken = errors.New("the onepassword service_account_token is not a 1Password service account token (they begin ops_)")

// signInAddress reads the sign-in address a service account token names,
// offline. The rest of the token's payload is key material; it is decoded
// here only to find the address and is not kept.
func signInAddress(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errMissingCredential
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return "", errNotAServiceAccountToken
	}
	payload := strings.TrimPrefix(token, tokenPrefix)
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(payload, "="))
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "=")); err != nil {
			return "", errNotAServiceAccountToken
		}
	}
	var fields struct {
		SignInAddress string `json:"signInAddress"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil || strings.TrimSpace(fields.SignInAddress) == "" {
		return "", errNotAServiceAccountToken
	}
	address := strings.TrimSpace(fields.SignInAddress)
	if !allowedHost(address) {
		return "", fmt.Errorf("the onepassword service account token signs in at %s, which is not a production 1Password domain (%s)", address, strings.Join(productionDomains, ", "))
	}
	return address, nil
}

// productionDomains are the 1Password domains this plugin will talk to. The
// SDK's own allow-list also admits 1Password's staging, development and test
// domains; a token naming one of those is refused here rather than followed.
var productionDomains = []string{"1password.com", "1password.ca", "1password.eu"}

func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range productionDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// requestTimeout bounds one 1Password API exchange. The SDK has no timeout of
// its own, and its WASM core cannot be interrupted mid-call, so this is what
// keeps an unresponsive endpoint from holding a resolve forever.
const requestTimeout = 20 * time.Second

// guardedTransport sends only HTTPS requests to production 1Password domains,
// over a transport that always verifies certificates.
type guardedTransport struct{ next http.RoundTripper }

func (g guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !allowedHost(req.URL.Hostname()) {
		return nil, fmt.Errorf("refused a request to %s://%s: this plugin talks to production 1Password domains over HTTPS only", req.URL.Scheme, req.URL.Hostname())
	}
	return g.next.RoundTrip(req)
}

func newTransport() http.RoundTripper {
	return guardedTransport{next: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		IdleConnTimeout:       90 * time.Second,
	}}
}

// installHTTP points the process at this plugin's HTTP client and log
// destination. Both are process-wide because that is where the SDK looks:
//
//   - The SDK's WASM core makes every request through a host function that
//     uses http.DefaultClient, read per request. Replacing it is the only way
//     to bound a request's time and restrict where it may go.
//   - The SDK logs through the standard library's log package. That already
//     defaults to stderr; it is set explicitly, because stdout is this
//     process's protocol stream and one log line there corrupts it.
func installHTTP(transport http.RoundTripper) {
	http.DefaultClient = &http.Client{Transport: transport, Timeout: requestTimeout + 10*time.Second}
	log.SetOutput(os.Stderr)
}

// sdkClient is a live SDK client narrowed to resolution.
//
// It holds the *onepassword.Client itself, not just its Secrets API. The SDK
// sets a finalizer on the Client that releases the session inside the WASM
// core, so a plugin that kept only client.Secrets() would have the session
// freed underneath it by the next garbage collection.
//
// Only Secrets().Resolve is ever called. The Client also carries the items,
// vaults, groups and environments APIs, which can create and delete; this
// type is the fence around them (TestNoCodeReachesAWriteAPI).
type sdkClient struct {
	client *onepassword.Client
}

func (c sdkClient) Resolve(ctx context.Context, ref string) (string, error) {
	return c.client.Secrets().Resolve(ctx, ref)
}

// newClient authenticates a service account and returns a client narrowed to
// resolution. Authentication is a network exchange, and the first client in a
// process also compiles the SDK's WASM core (about two seconds cold).
//
// It never uses the desktop-app integration, which loads the 1Password app's
// native library; the distributed binary is built without cgo, so it cannot.
func newClient(ctx context.Context, token string) (resolver, error) {
	client, err := onepassword.NewClient(ctx,
		onepassword.WithServiceAccountToken(token),
		onepassword.WithIntegrationInfo("Cerberus", Version),
	)
	if err != nil {
		return nil, err
	}
	return sdkClient{client: client}, nil
}
