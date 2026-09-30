package keeperplugin

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
	klog "github.com/keeper-security/secrets-manager-go/core/logger"
)

// errMissingCredential is what a resolve returns when the plugin's own
// credential did not arrive.
var errMissingCredential = errors.New("the keeper ksm_config was not supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health and
// by a resolve that cannot run. It is worded to survive the host's
// redact.Text: no "name: value" or "name=value" shapes (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Bind a Keeper Secrets Manager one-time access token with Keeper's own tooling, outside Cerberus, " +
	"then store the configuration it prints in the OS credential store with `cerberus secrets set keeper/ksm_config`, or supply it through " +
	envVar(SecretKSMConfig) + ", and reload the plugin with `cerberus connectors plugin managed load keeper`"

// referenceMarker replaces the reference in error text. A reference is not a
// credential, but a record title in one can be personal, and the host shows
// resolution errors to whoever ran the operation that needed the value.
const referenceMarker = "[reference]"

// heldConfigKeys are the configuration entries that are credential material.
var heldConfigKeys = []string{"clientId", "clientKey", "privateKey", "appKey"}

// Plugin serves Keeper Secrets Manager as a Cerberus secret backend over the
// plugin-sdk subprocess protocol.
type Plugin struct {
	config   subprocess.ConfigReader
	newVault func(config map[string]string) (vault, error)

	// busy serializes resolves: the SDK client rewrites its request context
	// on every call and is not safe for concurrent use. A channel rather than
	// a mutex, so a resolve waiting behind a slow one still honours its own
	// deadline.
	busy chan struct{}

	mu       sync.Mutex
	vault    vault
	hostname string
	problem  error
	scrub    scrubber
	held     []string
}

var (
	_ subprocess.Plugin         = (*Plugin)(nil)
	_ subprocess.HealthChecker  = (*Plugin)(nil)
	_ subprocess.MCPHandler     = (*Plugin)(nil)
	_ subprocess.CommandHandler = (*Plugin)(nil)
)

// New builds the plugin against the live Keeper API.
func New() *Plugin {
	return newPlugin(func(config map[string]string) (vault, error) {
		return newManager(config, newTransport())
	})
}

func newPlugin(newVault func(map[string]string) (vault, error)) *Plugin {
	quietKeeperLog()
	return &Plugin{newVault: newVault, busy: make(chan struct{}, 1)}
}

// quietKeeperLog points the SDK's logger at stderr, errors only. Its default
// is stdout, which is this process's protocol stream: one SDK log line there
// corrupts the channel the host reads results from. Stderr reaches the host,
// which forwards it through the plugin's redactor.
func quietKeeperLog() {
	klog.SetOutput(os.Stderr)
	klog.SetLogLevel(klog.ErrorLevel)
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Keeper Secret Backend",
		Version:     def.Version,
		Description: "Resolves keeper:// references through Keeper Secrets Manager",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing or unusable credential. The plugin starts,
// reports the gap in Health and status, and each resolve fails as
// credential_missing with the recovery named.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	raw := p.secret(SecretKSMConfig)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vault, p.hostname, p.problem = nil, "", nil
	p.held = []string{raw}
	config, err := parseConfig(raw)
	if err == nil {
		// The key material is removed from anything this plugin says: the
		// private key and app key are the credential, and the client id
		// identifies it. The hostname is not a credential; status shows it.
		for _, key := range heldConfigKeys {
			if value := config[key]; len(value) >= 8 {
				p.held = append(p.held, value)
			}
		}
		p.hostname = config["hostname"]
		p.vault, err = p.newVault(config)
	}
	p.problem = err
	p.scrub = newScrubber(p.held...)
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.vault = nil
	return nil
}

// Health makes no network call: it reports whether a bound configuration
// arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	status := p.status()
	return subprocess.HealthStatus{OK: status.Configured, Message: status.Message}, nil
}

func (p *Plugin) status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.vault != nil:
		return Status{Configured: true, Hostname: p.hostname, Scheme: Scheme,
			Message: "bound Keeper Secrets Manager configuration loaded; keeper:// references resolve against " + p.hostname}
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

// Command resolves one keeper:// reference. On success the result's Content
// is the value and nothing else. A failure is Action "error" whose Content is
// a coded error payload, the same shape a coded tool error carries, so the
// host reads the code rather than guessing from text.
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

// resolve asks Keeper for the one value ref names. Every call reaches Keeper:
// there is no cache here and none in the client (see newManager).
func (p *Plugin) resolve(ctx context.Context, ref string) (string, error) {
	p.mu.Lock()
	v, problem := p.vault, p.problem
	p.mu.Unlock()
	if v == nil {
		if problem == nil {
			problem = errMissingCredential
		}
		if errors.Is(problem, errMissingCredential) {
			return "", errors.New(credentialGuidance)
		}
		return "", problem
	}

	select {
	case p.busy <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("keeper resolve abandoned while another was in flight: %w", ctx.Err())
	}
	type answer struct {
		values []string
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		// The SDK takes no context. The transport bounds the request, and
		// the slot is released only when the SDK returns, so a request that
		// outlives its caller still holds off the next one.
		defer func() { <-p.busy }()
		values, err := v.GetNotationResults(ref)
		done <- answer{values, err}
	}()
	var got answer
	select {
	case got = <-done:
	case <-ctx.Done():
		return "", fmt.Errorf("keeper did not answer in time: %w", ctx.Err())
	}
	if got.err != nil {
		return "", fmt.Errorf("keeper could not resolve the reference: %w", got.err)
	}
	switch {
	case len(got.values) == 0:
		return "", errors.New("keeper resolved the reference to no value")
	case len(got.values) > 1:
		return "", fmt.Errorf("the reference names %d values; select one with an index, for example field/password[0]", len(got.values))
	case got.values[0] == "":
		return "", errors.New("keeper resolved the reference to an empty value")
	}
	value := got.values[0]
	p.hold(value)
	return value, nil
}

// hold adds a resolved value to what this plugin scrubs from everything it
// says afterwards, for the life of the load.
func (p *Plugin) hold(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = append(p.held, value)
	p.scrub = newScrubber(p.held...)
}

// errorText is err as the host may show it: every held value removed, and the
// reference, and the record uid or title inside it, replaced by a marker. The
// SDK quotes both back in its own errors ("multiple records match record
// '<title>'").
func (p *Plugin) errorText(err error, ref string) string {
	p.mu.Lock()
	scrub := p.scrub
	p.mu.Unlock()
	text := strings.ReplaceAll(scrub.text(err.Error()), ref, referenceMarker)
	record, _, _ := strings.Cut(strings.TrimPrefix(ref, Scheme+"://"), "/")
	if record = strings.TrimSpace(record); record != "" {
		text = strings.ReplaceAll(text, record, referenceMarker)
	}
	return text
}

func failure(code cerbplugin.ErrorCode, message string) subprocess.CommandResult {
	return subprocess.CommandResult{Action: "error", Content: string(cerbplugin.ErrorResult(code, message).Content)}
}
