package cfplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	cf "github.com/leefowlercu/go-contextforge/contextforge"
)

// DefaultAddress is ContextForge through a local SSH tunnel: 14444 is the
// tunnel's local end, forwarding to the gateway's own port on its host.
const DefaultAddress = "http://127.0.0.1:14444"

// SecretToken is the credential this plugin declares in its manifest, and the
// key the host hands the resolved value back under in init config. The two must
// agree, so they read from one constant.
const SecretToken = "token"

// ConfigAddress is the manifest config field naming the gateway base URL.
const ConfigAddress = "address"

// keychainService and keychainKey name where an operator stores the JWT:
// `keychain://contextforge/token`, go-keyring, login keychain, service
// "cerberus". The *host* reads it — Cerberus resolves every secret a plugin's
// manifest declares and passes the value in init config. They are named here
// only so a 401 can tell an operator where to put a token.
const (
	keychainService = "cerberus"
	keychainKey     = "contextforge/token"

	// TokenEnvVar is the direct-run fallback, for the binary invoked outside
	// the host with no init config. It does NOT reach the plugin under the
	// daemon: the host launches plugins with an allow-listed environment that
	// carries no credentials by design, which is exactly why the credential
	// travels in init config instead.
	TokenEnvVar = "CONTEXTFORGE_TOKEN"

	// AddressEnvVar overrides the gateway address for the same direct-run case.
	AddressEnvVar = "CONTEXTFORGE_ADDRESS"
)

// Backend is the seam that keeps a v0.x SDK swappable and the plugin testable
// without a network. Everything above it speaks in our own DTOs.
type Backend interface {
	ListGateways(ctx context.Context) ([]Gateway, error)
	ListVirtualServers(ctx context.Context) ([]VirtualServer, error)
	ListTools(ctx context.Context) ([]Tool, error)
	GetHealth(ctx context.Context) (Health, error)
}

type sdkBackend struct {
	address string
	client  *cf.Client
	http    *http.Client
}

var _ Backend = (*sdkBackend)(nil)

// NewSDKBackend builds a backend against the live gateway. An empty address
// falls back to the tunnel; an empty token still yields a usable backend
// because /health is open, and the auth'd calls report a clear 401.
func NewSDKBackend(address, token string) (Backend, error) {
	if address == "" {
		address = DefaultAddress
	}
	if _, err := url.Parse(address); err != nil {
		return nil, fmt.Errorf("invalid contextforge address %q: %w", address, err)
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	client, err := cf.NewClient(httpClient, address, token)
	if err != nil {
		return nil, fmt.Errorf("build contextforge client: %w", err)
	}
	return &sdkBackend{address: address, client: client, http: httpClient}, nil
}

// ResolveAddress prefers an explicit override, then the tunnel default.
func ResolveAddress() string {
	if addr := os.Getenv(AddressEnvVar); addr != "" {
		return addr
	}
	return DefaultAddress
}

func (b *sdkBackend) ListGateways(ctx context.Context) ([]Gateway, error) {
	gateways, _, err := b.client.Gateways.List(ctx, nil)
	if err != nil {
		return nil, b.describeError("list gateways", err)
	}
	return GatewaysFromSDK(gateways), nil
}

func (b *sdkBackend) ListVirtualServers(ctx context.Context) ([]VirtualServer, error) {
	servers, _, err := b.client.Servers.List(ctx, nil)
	if err != nil {
		return nil, b.describeError("list virtual servers", err)
	}
	return VirtualServersFromSDK(servers), nil
}

func (b *sdkBackend) ListTools(ctx context.Context) ([]Tool, error) {
	tools, _, err := b.client.Tools.List(ctx, nil)
	if err != nil {
		return nil, b.describeError("list tools", err)
	}
	return ToolsFromSDK(tools), nil
}

// GetHealth calls /health directly: it is the one open endpoint and the SDK
// does not expose it.
func (b *sdkBackend) GetHealth(ctx context.Context) (Health, error) {
	endpoint := strings.TrimSuffix(b.address, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Health{}, fmt.Errorf("build health request: %w", err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return Health{}, b.describeError("get health", err)
	}
	defer func() { _ = resp.Body.Close() }()

	health := Health{OK: resp.StatusCode == http.StatusOK, Address: b.address}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
		health.Status = body.Status
	}
	if !health.OK {
		health.Status = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return health, nil
}

// describeError names the tunnel when the tunnel is what failed. A refused
// connection on the local tunnel port means the tunnel is down, not the
// gateway — telling an operator "gateway is down" sends them to the wrong host.
func (b *sdkBackend) describeError(action string, err error) error {
	if err == nil {
		return nil
	}
	if isUnauthorized(err) {
		return cerbplugin.WithCode(cerbplugin.ErrorCredentialMissing, fmt.Errorf(
			// Deliberately avoids the word "bearer" followed by a word: the
			// host redacts /(?i)\bBearer[ \t]+\S+/ on every error path, which
			// would garble the very instruction this message carries.
			"%s at %s: unauthorized (401). ContextForge requires a JWT — an API key or a raw token is rejected. "+
				"Store a token at keychain://%s (go-keyring service %q) and reload the plugin — Cerberus resolves it and hands it over — "+
				"or set %s when running the plugin directly: %w",
			action, b.address, keychainKey, keychainService, TokenEnvVar, err))
	}
	// Coded unreachable, so an agent branching on the code tells a down
	// tunnel from a down gateway from a missing token. The host would
	// otherwise report any failure of a plugin that loaded without its JWT as
	// credential_missing, which is exactly wrong here: /health needs no token.
	if isConnectionRefused(err) && isLoopback(b.address) {
		return cerbplugin.WithCode(cerbplugin.ErrorUnavailable, fmt.Errorf(
			"%s: cannot reach ContextForge at %s — the tunnel is down, not the gateway. "+
				"Start the tunnel resource that forwards it (`cerberus resource start <tunnel>`; it may need a VPN): %w",
			action, b.address, err))
	}
	if isUnreachable(err) {
		return cerbplugin.WithCode(cerbplugin.ErrorUnavailable, fmt.Errorf("%s: cannot reach ContextForge at %s: %w", action, b.address, err))
	}
	return fmt.Errorf("%s at %s: %w", action, b.address, err)
}

// isUnreachable matches a network-level failure to reach the gateway at all:
// a refused connection, a name that does not resolve, a timeout. An HTTP
// status is not one of these; the gateway answered.
func isUnreachable(err error) bool {
	if isConnectionRefused(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// isUnauthorized detects a 401 so the operator is told how to supply a token
// rather than being left to infer it from a bare status code.
func isUnauthorized(err error) bool {
	var apiErr *cf.ErrorResponse
	if errors.As(err, &apiErr) && apiErr.Response != nil {
		return apiErr.Response.StatusCode == http.StatusUnauthorized
	}
	return strings.Contains(err.Error(), "; 401")
}

// isConnectionRefused matches a refused connection specifically. A timeout or a
// DNS failure is a different problem and must not be reported as a down tunnel.
func isConnectionRefused(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	return strings.Contains(err.Error(), "connection refused")
}

func isLoopback(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
