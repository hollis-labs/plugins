package ghplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// errMissingCredential is what every GitHub call returns when no token
// arrived. It is short on purpose: the host recognises a failure from a plugin
// that loaded without a required secret and appends its own guidance naming
// every way to supply one, so repeating it here would print it twice.
var errMissingCredential = errors.New("no GitHub API token was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes, no
// flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply it as " + TokenEnvVar +
	", as the token entry under github in connector-secrets.yaml, or as keychain://github/token " +
	"(`cerberus secrets set github/token` stores it there), " +
	"then reload the plugin with `cerberus connectors plugin managed load github`"

// Plugin serves the GitHub connector over the plugin-sdk subprocess protocol.
type Plugin struct {
	newBackend func(token string) Backend
	config     subprocess.ConfigReader
	backend    Backend
	scrub      scrubber
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live GitHub API. The token is not looked
// up here: Cerberus resolves every secret this manifest declares and hands the
// values over in init config.
//
// There is no gh CLI fallback, unlike the compiled-in connector: gh
// authenticates with its own login under HOME, a credential outside the
// declared-secret channel.
func New() *Plugin { return &Plugin{newBackend: newBackend} }

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(string) Backend { return backend },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus GitHub Connector",
		Version:     def.Version,
		Description: "GitHub repository status, releases and Actions runs for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing token. The plugin starts, reports the gap in
// Health, and each GitHub call fails with errMissingCredential.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	token := p.token()
	p.scrub = newScrubber(token)
	if p.backend == nil && token != "" {
		p.backend = p.newBackend(token)
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// Health makes no network call: it reports whether a credential arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: credentialGuidance}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "API token configured"}, nil
}

// token reads the credential the host resolved, falling back to the
// environment only for a binary run directly, outside the host.
func (p *Plugin) token() string {
	if p.config != nil {
		if token := p.config.Secret(SecretToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		return result, nil
	}
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, p.scrub.text(err.Error())), nil
	}
	return result, p.scrub.err(err)
}

// errorCode is the Cerberus code for a failed call. A missing token and a
// token GitHub rejects (401) are credential_missing; an API that cannot be
// reached at all is unavailable; arguments this plugin refused, and a
// repository GitHub does not show this token (404), are invalid_args.
// Anything else GitHub answered, a 403 or a rate limit included, stays
// uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *apiError
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr):
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			return cerbplugin.ErrorCredentialMissing
		case http.StatusNotFound:
			return cerbplugin.ErrorInvalidArgs
		}
		return ""
	case isUnreachable(err):
		return cerbplugin.ErrorUnavailable
	}
	return ""
}

// isUnreachable matches a network-level failure: a refused connection, a
// name that does not resolve, a timeout. An HTTP status is not one of these.
func isUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
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

func (p *Plugin) call(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	args := newArgMap(req.Arguments)
	if args.boolean(argDryRun) {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s is read-only and has no dry-run preview; run it without --dry-run", op.Name))
	}

	owner, repo := args.ownerRepo()
	limit := defaultLimit
	if op.Name != "status" {
		limit = args.limit()
	}
	if err := args.err(); err != nil {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
	}
	if p.backend == nil {
		return subprocess.MCPCallResult{}, errMissingCredential
	}

	switch op.Name {
	case "status":
		return marshalResult(p.backend.RepoStatus(ctx, owner, repo))
	case "list_releases":
		return marshalResult(p.backend.ListReleases(ctx, owner, repo, limit))
	case "list_workflow_runs":
		return marshalResult(p.backend.ListWorkflowRuns(ctx, owner, repo, limit))
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

func marshalResult[T any](data T, err error) (subprocess.MCPCallResult, error) {
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
