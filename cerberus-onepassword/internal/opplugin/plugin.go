package opplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// errMissingCredential is what a resolve returns when the plugin's own
// credential did not arrive.
var errMissingCredential = errors.New("the onepassword service_account_token was not supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health and
// by a resolve that cannot run. It is worded to survive the host's
// redact.Text: no "name: value" or "name=value" shapes (see plugin_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Create a 1Password service account with read access to only the vaults Cerberus may read, " +
	"then store its token in the OS credential store with `cerberus secrets set onepassword/service_account_token`, or supply it through " +
	envVar(SecretServiceAccountToken) + ", and reload the plugin with `cerberus connectors plugin managed load onepassword`"

// referenceMarker replaces the reference in error text. A reference is not a
// credential, but vault and item names in one can be personal, and the host
// shows resolution errors to whoever ran the operation that needed the value.
const referenceMarker = "[reference]"

// Plugin serves 1Password as a Cerberus secret backend over the plugin-sdk
// subprocess protocol.
type Plugin struct {
	config    subprocess.ConfigReader
	newClient func(ctx context.Context, token string) (resolver, error)

	// busy serializes resolves: the SDK's WASM core is single-threaded. A
	// channel rather than a mutex, so a resolve waiting behind a slow one
	// still honours its own deadline.
	busy chan struct{}

	mu      sync.Mutex
	token   string
	address string
	problem error
	client  resolver
	scrub   scrubber
	held    []string
}

var (
	_ subprocess.Plugin         = (*Plugin)(nil)
	_ subprocess.HealthChecker  = (*Plugin)(nil)
	_ subprocess.MCPHandler     = (*Plugin)(nil)
	_ subprocess.CommandHandler = (*Plugin)(nil)
)

// New builds the plugin against the live 1Password API.
func New() *Plugin {
	installHTTP(newTransport())
	return newPlugin(newClient)
}

func newPlugin(newClient func(context.Context, string) (resolver, error)) *Plugin {
	return &Plugin{newClient: newClient, busy: make(chan struct{}, 1)}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus 1Password Secret Backend",
		Version:     def.Version,
		Description: "Resolves op:// references through a 1Password service account",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load checks the token offline and never fails on it. The SDK client, which
// authenticates over the network, is made at the first resolve, so a load
// costs no network call and no WASM compile. The plugin reports a missing or
// malformed token in Health and status, and each resolve fails as
// credential_missing with the recovery named.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	token := strings.TrimSpace(p.secret(SecretServiceAccountToken))
	address, err := signInAddress(token)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token, p.address, p.problem, p.client = "", "", err, nil
	if err == nil {
		p.token, p.address = token, address
	}
	p.held = []string{token}
	p.scrub = newScrubber(p.held...)
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client, p.token = nil, ""
	return nil
}

// Health makes no network call: it reports whether a usable token arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	status := p.status()
	return subprocess.HealthStatus{OK: status.Configured, Message: status.Message}, nil
}

func (p *Plugin) status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.token != "":
		return Status{Configured: true, SignInAddress: p.address, Scheme: Scheme,
			Message: "service account token loaded; op:// references resolve against " + p.address}
	case p.problem == nil || errors.Is(p.problem, errMissingCredential):
		return Status{Scheme: Scheme, Message: credentialGuidance}
	default:
		return Status{Scheme: Scheme, Message: p.scrub.text(p.problem.Error())}
	}
}

// secret reads what the host resolved, falling back to the environment only
// for a binary run directly, outside the host.
func (p *Plugin) secret(name string) string {
	if p.config != nil {
		if value := p.config.Secret(name); value != "" {
			return value
		}
	}
	return os.Getenv(envVar(name))
}

// MCPCallTool serves the connector's operations: status, and nothing else.
// Resolution is not a tool.
func (p *Plugin) MCPCallTool(_ context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok || op.Name != "status" {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	content, err := json.Marshal(p.status())
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}

// resolveArgs is the command/execute argument the host sends.
type resolveArgs struct {
	Ref string `json:"ref"`
}

// Command resolves one op:// reference. On success the result's Content is
// the value and nothing else. A failure is Action "error" whose Content is a
// coded error payload, the same shape a coded tool error carries, so the host
// reads the code rather than guessing from text.
func (p *Plugin) Command(ctx context.Context, req subprocess.CommandRequest) (subprocess.CommandResult, error) {
	if req.Name != ResolveCommand {
		return failure(cerbplugin.ErrorInvalidArgs, fmt.Sprintf("unknown command %q; this plugin serves only %s", req.Name, ResolveCommand)), nil
	}
	var args resolveArgs
	if err := json.Unmarshal([]byte(req.Args), &args); err != nil {
		return failure(cerbplugin.ErrorInvalidArgs, "the resolve arguments are not a JSON object with a ref"), nil
	}
	ref := strings.TrimSpace(args.Ref)
	if !strings.HasPrefix(ref, Scheme+"://") {
		return failure(cerbplugin.ErrorInvalidArgs, "this plugin resolves only "+Scheme+":// references"), nil
	}
	value, err := p.resolve(ctx, ref)
	if err != nil {
		return failure(cerbplugin.ErrorCredentialMissing, p.errorText(err, ref)), nil
	}
	return subprocess.CommandResult{Action: "message", Content: value}, nil
}

// resolve asks 1Password for the value ref names. Every call reaches
// 1Password: the client is reused, which keeps its session, but no value is
// kept here, and a failed call drops the client so the next one
// authenticates afresh rather than trusting a session that may be the cause.
func (p *Plugin) resolve(ctx context.Context, ref string) (string, error) {
	p.mu.Lock()
	token, problem := p.token, p.problem
	p.mu.Unlock()
	if token == "" {
		if problem == nil || errors.Is(problem, errMissingCredential) {
			return "", errors.New(credentialGuidance)
		}
		return "", problem
	}

	select {
	case p.busy <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("1Password resolve abandoned while another was in flight: %w", ctx.Err())
	}
	type answer struct {
		value string
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		// The WASM core cannot be interrupted, so the slot is released only
		// when the SDK returns; the HTTP client bounds how long that is.
		defer func() { <-p.busy }()
		client, err := p.clientFor(ctx, token)
		if err != nil {
			done <- answer{err: fmt.Errorf("1Password sign-in failed: %w", err)}
			return
		}
		value, err := client.Resolve(ctx, ref)
		if err != nil {
			p.dropClient(client)
		}
		done <- answer{value, err}
	}()
	var got answer
	select {
	case got = <-done:
	case <-ctx.Done():
		return "", fmt.Errorf("1Password did not answer in time: %w", ctx.Err())
	}
	if got.err != nil {
		if strings.HasPrefix(got.err.Error(), "1Password sign-in failed") {
			return "", got.err
		}
		return "", fmt.Errorf("1Password could not resolve the reference: %w", got.err)
	}
	if got.value == "" {
		return "", errors.New("1Password resolved the reference to an empty value")
	}
	p.hold(got.value)
	return got.value, nil
}

// clientFor returns the current client, signing in first if there is none.
func (p *Plugin) clientFor(ctx context.Context, token string) (resolver, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client != nil {
		return client, nil
	}
	client, err := p.newClient(ctx, token)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.client = client
	p.mu.Unlock()
	return client, nil
}

func (p *Plugin) dropClient(client resolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == client {
		p.client = nil
	}
}

// hold adds a resolved value to what this plugin scrubs from everything it
// says afterwards, for the life of the load.
func (p *Plugin) hold(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = append(p.held, value)
	p.scrub = newScrubber(p.held...)
}

// errorText is err as the host may show it:
//   - its first line only, since the SDK appends a WASM stack trace to an
//     error raised inside its core;
//   - every held value removed;
//   - the reference, and the vault and item names inside it, replaced by a
//     marker, because the SDK quotes them back in its own errors.
func (p *Plugin) errorText(err error, ref string) string {
	p.mu.Lock()
	scrub := p.scrub
	p.mu.Unlock()
	text, _, _ := strings.Cut(err.Error(), "\n")
	text = strings.TrimSpace(strings.ReplaceAll(scrub.text(text), ref, referenceMarker))
	parts := strings.Split(strings.TrimPrefix(ref, Scheme+"://"), "/")
	if len(parts) > 2 {
		parts = parts[:2] // vault and item; the field name is not personal
	}
	for _, part := range parts {
		if part = strings.TrimSpace(part); len(part) >= 3 {
			text = strings.ReplaceAll(text, part, referenceMarker)
		}
	}
	return text
}

func failure(code cerbplugin.ErrorCode, message string) subprocess.CommandResult {
	return subprocess.CommandResult{Action: "error", Content: string(cerbplugin.ErrorResult(code, message).Content)}
}
