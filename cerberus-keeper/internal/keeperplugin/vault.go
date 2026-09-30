package keeperplugin

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	ksm "github.com/keeper-security/secrets-manager-go/core"
	klog "github.com/keeper-security/secrets-manager-go/core/logger"
)

// vault is the one thing this plugin asks of Keeper: the values a notation
// names. *ksm.SecretsManager satisfies it.
type vault interface {
	GetNotationResults(notation string) ([]string, error)
}

// requiredConfigKeys are what a *bound* Secrets Manager configuration holds.
// A configuration still carrying a one-time token instead is unbound, and
// binding it is a write this plugin never performs: it would consume the
// token and produce a new credential nobody could store.
var requiredConfigKeys = []string{"hostname", "clientId", "privateKey", "appKey"}

// errUnbound is the refusal for a configuration that would need binding.
var errUnbound = errors.New("the keeper ksm_config is not a bound Secrets Manager configuration. Bind the one-time access token with Keeper's own tooling, " +
	"outside Cerberus, and store the configuration it prints")

// parseConfig reads a bound configuration in either form Keeper prints it:
// base64 of the JSON, or the JSON itself.
//
// The SDK's own string parser logs the first sixteen characters of a
// configuration it cannot read, so this never hands it a string: it parses
// here and passes a map. An error names what is missing, never a value.
func parseConfig(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errMissingCredential
	}
	text := raw
	if !strings.HasPrefix(raw, "{") {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, errors.New("the keeper ksm_config is neither JSON nor base64 of JSON")
		}
		text = string(decoded)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return nil, errors.New("the keeper ksm_config is not a JSON object")
	}
	config := make(map[string]string, len(fields))
	for key, value := range fields {
		if s, ok := value.(string); ok {
			config[key] = s
		}
	}
	var missing []string
	for _, key := range requiredConfigKeys {
		if strings.TrimSpace(config[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("%w (missing %s)", errUnbound, strings.Join(missing, ", "))
	}
	return config, nil
}

// requestTimeout bounds one Keeper API exchange. The SDK's own client has no
// timeout at all, so an unresponsive endpoint would hold a resolve forever.
const requestTimeout = 20 * time.Second

// newTransport is the HTTP transport every Keeper request uses. It always
// verifies TLS, whatever KSM_SKIP_VERIFY says in the environment, and it
// bounds every phase of a request.
func newTransport() http.RoundTripper {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		IdleConnTimeout:       90 * time.Second,
	}
}

// newManager builds the Secrets Manager client this plugin resolves through.
// It is the one place a client is made, and what it deliberately leaves out
// is the point:
//
//   - No cache. The SDK's cache (SetCache, the file cache) serves the last
//     good response when Keeper cannot be reached, with nothing but a warning
//     in a log. A secret backend that fails must fail as credential_missing;
//     a stale credential served silently is the worst answer it can give.
//     TestAFailingTransportNeverServesAPriorValue holds this.
//   - No configuration from the environment or a file. The SDK reads
//     KSM_CONFIG, and writes client-config.json in the working directory,
//     when it is handed no configuration. It is always handed the map.
//   - No TLS opt-out. The transport is ours, through the SDK's Context
//     seam, and verifies certificates; VerifySslCerts is forced on as well.
//
// transport is the round tripper requests go through.
func newManager(config map[string]string, transport http.RoundTripper) (*ksm.SecretsManager, error) {
	keeperContext := &ksm.Context{Transport: transport}
	return newManagerWithContext(config, &keeperContext)
}

// newManagerWithContext is newManager with the SDK's request context supplied
// by the caller, which is how a test's transport sees the per-request
// transmission key it must encrypt a response with. The SDK rewrites
// *keeperContext on every request and keeps its Transport.
func newManagerWithContext(config map[string]string, keeperContext **ksm.Context) (*ksm.SecretsManager, error) {
	sm := ksm.NewSecretsManager(&ksm.ClientOptions{
		Config:   ksm.NewMemoryKeyValueStorage(config),
		LogLevel: klog.ErrorLevel,
	}, keeperContext)
	if sm == nil {
		return nil, errors.New("the keeper ksm_config was refused by the Secrets Manager client")
	}
	sm.VerifySslCerts = true
	return sm, nil
}
