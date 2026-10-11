package tailscaleplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// errMissingCredential is what every API call returns when no usable credential
// arrived. It is short on purpose: the host appends its own guidance for a
// plugin that loaded without a declared secret, so repeating it would print it
// twice. The local-CLI operations never return it.
var errMissingCredential = errors.New("no Tailscale API credential was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health. It
// is worded to survive the host's redact.Text: no name=value or name: value
// shapes, no flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply either " + APIKeyEnvVar + " or both " +
	OAuthClientIDEnvVar + " and " + OAuthClientSecretEnvVar +
	", or the matching api_key, oauth_client_id and oauth_client_secret entries under tailscale in connector-secrets.yaml, " +
	"then reload the plugin with `cerberus connectors plugin managed load tailscale`"

// localOps are the operations that drive the tailscale CLI on this host. They
// need no API credential.
var localOps = map[string]bool{"status": true, "up": true, "down": true, "serve": true}

// Plugin serves the Tailscale connector over the plugin-sdk subprocess
// protocol: the local CLI operations, and the control-plane API operations
// through a Backend.
type Plugin struct {
	newBackend func(Credentials) Backend
	config     subprocess.ConfigReader
	backend    Backend

	mu    sync.Mutex
	scrub scrubber
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live Tailscale API. Credentials are not
// looked up here: Cerberus resolves every secret the manifest declares and
// hands the values over in init config.
func New() *Plugin {
	return &Plugin{newBackend: func(c Credentials) Backend { return NewHTTPBackend("", c) }}
}

// NewWithBackend is the test seam.
func NewWithBackend(backend Backend) *Plugin {
	return &Plugin{
		newBackend: func(Credentials) Backend { return backend },
		backend:    backend,
	}
}

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	return subprocess.InitResult{
		ID:          ConnectorID,
		Name:        "Cerberus Tailscale Connector",
		Version:     Version,
		Description: "Tailscale local CLI and control-plane API administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing credential. The plugin starts, Health says so,
// and each API call fails with errMissingCredential. The local operations and
// dry-run mints need none.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	creds := p.credentials()
	p.mu.Lock()
	p.scrub = newScrubber(creds.APIKey, creds.OAuthClientID, creds.OAuthClientSecret)
	p.mu.Unlock()
	if p.backend == nil && creds.Present() {
		p.backend = p.newBackend(creds)
	}
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.backend = nil
	return nil
}

// credentials reads what the host resolved, falling back to the environment
// only for a binary run directly, outside the host, which receives no init
// config.
func (p *Plugin) credentials() Credentials {
	read := func(name, env string) string {
		if p.config != nil {
			if v := p.config.Secret(name); v != "" {
				return v
			}
		}
		return os.Getenv(env)
	}
	return Credentials{
		APIKey:            read(SecretAPIKey, APIKeyEnvVar),
		OAuthClientID:     read(SecretOAuthClientID, OAuthClientIDEnvVar),
		OAuthClientSecret: read(SecretOAuthClientSecret, OAuthClientSecretEnvVar),
	}
}

// configuredTailnet is the tailnet from config, or the credential's own.
func (p *Plugin) configuredTailnet() string {
	if p.config != nil {
		if v := strings.TrimSpace(p.config.String(ConfigTailnet)); v != "" {
			return v
		}
	}
	return DefaultTailnet
}

// Health makes no network call. The plugin is healthy if it can do anything
// at all: the tailscale CLI is on this host, or an API credential arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	_, cliErr := resolveTailscale()
	cli := cliErr == nil
	api := p.backend != nil
	switch {
	case cli && api:
		return subprocess.HealthStatus{OK: true, Message: "tailscale CLI found; API credential configured"}, nil
	case cli:
		return subprocess.HealthStatus{OK: true, Message: "tailscale CLI found; API operations are unavailable. " + credentialGuidance}, nil
	case api:
		return subprocess.HealthStatus{OK: true, Message: "API credential configured; tailscale binary not found, so the local operations are unavailable"}, nil
	}
	return subprocess.HealthStatus{OK: false, Message: "tailscale binary not found. " + credentialGuidance}, nil
}

// ackOperations are the API operations that change the tailnet, and so need
// acknowledgment; every one of them has a dry-run preview. The contract says
// the same thing, and TestGatesMatchTheContract holds the two in step.
var ackOperations = map[string]bool{
	"mint_auth_key":    true,
	"revoke_key":       true,
	"delete_device":    true,
	"set_tags":         true,
	"authorize_device": true,
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
	}
	if localOps[op.Name] {
		return p.callLocal(ctx, op.Name, req.Arguments)
	}

	result, err := p.callAPI(ctx, op.Name, newArgMap(req.Arguments))
	scrub := p.scrubber()
	if err == nil {
		return withOperationTelemetry(req, result), nil
	}
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, scrub.text(err.Error())), nil
	}
	return result, scrub.err(err)
}

func (p *Plugin) scrubber() scrubber {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scrub
}

// register adds a value minted during this process to what is scrubbed from
// every later error, so a key that came back once cannot surface again in text
// composed from a later failure. Cerberus's own request scope cannot learn it:
// a plugin has no channel to hand the host a value at call time, which is why
// the result field is the only place it appears.
func (p *Plugin) register(secret string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scrub = p.scrub.with(secret)
}

// errorCode is the Cerberus code for a failed call. A missing credential and
// one Tailscale rejects (401) are credential_missing; an API that cannot be
// reached is unavailable; arguments this plugin refused, and an id Tailscale
// does not know (404) or a body it refused (400), are invalid_args. Anything
// else the API answered, a 403 included, stays uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *apiError
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr):
		switch apiErr.Status {
		case http.StatusUnauthorized:
			return cerbplugin.ErrorCredentialMissing
		case http.StatusNotFound, http.StatusBadRequest:
			return cerbplugin.ErrorInvalidArgs
		}
		return ""
	case isUnreachable(err):
		return cerbplugin.ErrorUnavailable
	}
	return ""
}

// isUnreachable matches a network-level failure: a refused connection, a name
// that does not resolve, a timeout. An HTTP status is not one of these.
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

var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	tagPattern     = regexp.MustCompile(`^tag:[A-Za-z][A-Za-z0-9-]{0,63}$`)
	tailnetPattern = regexp.MustCompile(`^(-|[A-Za-z0-9][A-Za-z0-9_.@-]{0,127})$`)
)

// id reads a required identifier that becomes a URL path segment, so only a
// plain token is accepted.
func (a *argMap) id(key string) string {
	v := a.required(key)
	if v != "" && !idPattern.MatchString(v) {
		a.problems = append(a.problems, key+" must be a plain id (letters, digits, '.', '_', '-')")
		return ""
	}
	return v
}

// tags reads a list of tags, each tag:name. required says an empty list is an
// error.
func (a *argMap) tags(key string, required bool) []string {
	tags := a.strSlice(key)
	for _, tag := range tags {
		if !tagPattern.MatchString(tag) {
			a.problems = append(a.problems, fmt.Sprintf("%s entry %q must look like tag:name", key, tag))
		}
	}
	if required && len(tags) == 0 {
		a.problems = append(a.problems, key+" is required")
	}
	return dedupe(tags)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// boolDefault reads a boolean that is def when the caller said nothing.
func (a *argMap) boolDefault(key string, def bool) bool {
	if v, ok := a.raw[key]; !ok || v == nil || a.str(key) == "" {
		return def
	}
	return a.boolean(key)
}

// tailnet is the per-call tailnet, else the configured one.
func (p *Plugin) tailnet(a *argMap) string {
	if v := a.str(ConfigTailnet); v != "" {
		if !tailnetPattern.MatchString(v) {
			a.problems = append(a.problems, "tailnet must be a tailnet name or id, or '-'")
			return ""
		}
		return v
	}
	return p.configuredTailnet()
}

func (p *Plugin) callAPI(ctx context.Context, name string, args *argMap) (subprocess.MCPCallResult, error) {
	dryRun := false
	if args.boolean(argDryRun) && !ackOperations[name] {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s has no dry-run preview; run it without --dry-run", name))
	}
	if ackOperations[name] {
		var err error
		if dryRun, err = writeMode(args, name); err != nil {
			return subprocess.MCPCallResult{}, err
		}
	}

	switch name {
	case "list_auth_keys":
		tailnet := p.tailnet(args)
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListAuthKeys(ctx, tailnet))

	case "mint_auth_key":
		tailnet := p.tailnet(args)
		req := MintRequest{
			Tags:          args.tags("tags", true),
			Description:   args.str("description"),
			Reusable:      args.boolean("reusable"),
			Ephemeral:     args.boolDefault("ephemeral", true),
			Preauthorized: args.boolDefault("preauthorized", true),
			TTL:           defaultMintTTL,
		}
		if ttl := args.intPtr("ttl_seconds"); ttl != nil {
			req.TTL = time.Duration(*ttl) * time.Second
		}
		if len(req.Description) > 50 {
			args.problems = append(args.problems, "description must be 50 characters or fewer")
		}
		if req.TTL < minMintTTL || req.TTL > maxMintTTL {
			args.problems = append(args.problems, "ttl_seconds must be between 60 and 7776000 (90 days)")
		}
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(mintPreview(tailnet, req), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		minted, err := p.backend.CreateAuthKey(ctx, tailnet, req)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		// Registered before the result is built, so nothing composed after
		// this point can carry the value into an error.
		p.register(minted.Key)
		return marshalResult(minted, nil)

	case "revoke_key":
		tailnet := p.tailnet(args)
		id := args.id("key_id")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			key, err := p.backend.GetAuthKey(ctx, tailnet, id)
			if err != nil {
				return subprocess.MCPCallResult{}, err
			}
			return marshalResult(revokePreview(tailnet, *key), nil)
		}
		if err := p.backend.RevokeAuthKey(ctx, tailnet, id); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(KeyAction{KeyID: id, Action: "revoke"}, nil)

	case "list_devices":
		tailnet := p.tailnet(args)
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListDevices(ctx, tailnet))

	case "get_device":
		id := args.id("device_id")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetDevice(ctx, id))

	case "delete_device", "authorize_device", "set_tags":
		id := args.id("device_id")
		var tags []string
		if name == "set_tags" {
			tags = args.tags("tags", false)
		}
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			device, err := p.backend.GetDevice(ctx, id)
			if err != nil {
				return subprocess.MCPCallResult{}, err
			}
			return marshalResult(devicePreview(name, *device, tags), nil)
		}
		var err error
		action := DeviceAction{DeviceID: id}
		switch name {
		case "delete_device":
			action.Action, err = "delete", p.backend.DeleteDevice(ctx, id)
		case "authorize_device":
			action.Action, err = "authorize", p.backend.AuthorizeDevice(ctx, id)
		case "set_tags":
			action.Action, action.Tags = "set_tags", tags
			err = p.backend.SetDeviceTags(ctx, id, tags)
		}
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(action, nil)

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", name)
	}
}

func mintPreview(tailnet string, req MintRequest) DryRunPreview {
	var warnings []string
	if req.Reusable {
		warnings = append(warnings, "the key is reusable: any number of devices can join with it until it expires or is revoked")
	}
	if req.TTL > 24*time.Hour {
		warnings = append(warnings, "the key lives longer than a day; a short lifetime limits the damage if it leaks")
	}
	if !req.Ephemeral {
		warnings = append(warnings, "devices that join are not ephemeral and stay on the tailnet until removed")
	}
	warnings = append(warnings, "the key value is returned once, in the result of the real call, and cannot be read again")
	return newPreview("mint_auth_key", "Would mint a Tailscale auth key. No key is created by this preview.",
		map[string]any{"tailnet": tailnet},
		map[string]any{
			"tags":          req.Tags,
			"description":   req.Description,
			"reusable":      req.Reusable,
			"ephemeral":     req.Ephemeral,
			"preauthorized": req.Preauthorized,
			"ttl_seconds":   int64(req.TTL / time.Second),
		}, warnings...)
}

func revokePreview(tailnet string, key AuthKey) DryRunPreview {
	var warnings []string
	if key.Revoked != nil || key.Invalid {
		warnings = append(warnings, "this key is already revoked or no longer valid")
	}
	if key.KeyType != "" && key.KeyType != "auth" {
		warnings = append(warnings, "this is a "+key.KeyType+" credential, not an auth key; revoking it may cut off whatever uses it to call the API")
	}
	return newPreview("revoke_key", "Would revoke a Tailscale key. It stops working at once; devices that already joined with it stay.",
		map[string]any{"tailnet": tailnet, "key_id": key.ID},
		map[string]any{"key": key}, warnings...)
}

func devicePreview(name string, device Device, tags []string) DryRunPreview {
	target := map[string]any{"device_id": device.ID, "hostname": device.Hostname}
	switch name {
	case "delete_device":
		var warnings []string
		if len(device.Tags) > 0 {
			warnings = append(warnings, "this device is tagged; it needs a new key to rejoin")
		}
		return newPreview(name, "Would remove a device from the tailnet.", target, map[string]any{"device": device}, warnings...)
	case "authorize_device":
		var warnings []string
		if device.Authorized {
			warnings = append(warnings, "this device is already authorized; the call would change nothing")
		}
		return newPreview(name, "Would authorize a device, admitting it to the tailnet.", target, map[string]any{"authorized": device.Authorized}, warnings...)
	}
	var warnings []string
	if len(tags) == 0 {
		warnings = append(warnings, "this clears every tag: the device stops matching tag-based ACL rules")
	}
	if equalSets(device.Tags, tags) {
		warnings = append(warnings, "the device already has exactly these tags; the call would change nothing")
	}
	return newPreview(name, "Would replace a device's tags.", target,
		map[string]any{"current_tags": device.Tags, "new_tags": tags, "added": difference(tags, device.Tags), "removed": difference(device.Tags, tags)},
		warnings...)
}

func difference(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

func equalSets(a, b []string) bool {
	return len(difference(a, b)) == 0 && len(difference(b, a)) == 0
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses an acknowledgment-gated operation without --ack
// before the call reaches this process, so under the host this never fires. It
// is here for the binary driven any other way, so that the plugin's own
// contract is "no unacknowledged write". A dry run changes nothing and needs no
// acknowledgment.
func writeMode(args *argMap, operation string) (bool, error) {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		return false, fmt.Errorf("%s changes the tailnet and requires acknowledgment. Re-run it acknowledged (--ack), or preview it first with --dry-run", operation)
	}
	return dryRun, nil
}

// invalid codes this plugin's own refusal of the caller's arguments.
func invalid(err error) error {
	if err == nil {
		return nil
	}
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
}

func (p *Plugin) ready() error {
	if p.backend == nil {
		return errMissingCredential
	}
	return nil
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

// --- local CLI operations ---

func (p *Plugin) callLocal(ctx context.Context, name string, args map[string]any) (subprocess.MCPCallResult, error) {
	switch name {
	case "status":
		return p.runTailscale(ctx, []string{"status"})
	case "up":
		return p.runTailscale(ctx, []string{"up"})
	case "down":
		return p.runTailscale(ctx, []string{"down"})
	default: // serve
		port, _ := args["port"].(string)
		funnel, _ := args["funnel"].(bool)
		if funnel {
			return p.runTailscale(ctx, []string{"funnel", "--bg", port})
		}
		return p.runTailscale(ctx, []string{"serve", "--bg", port})
	}
}

// resolveTailscale finds the tailscale binary on every call. The daemon's PATH
// is minimal, so after PATH it tries the usual install locations.
func resolveTailscale() (string, error) {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path, nil
	}
	for _, candidate := range []string{
		"/usr/bin/tailscale",
		"/usr/local/bin/tailscale",
		"/opt/homebrew/bin/tailscale",
		"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("tailscale binary not found")
}

func (p *Plugin) runTailscale(ctx context.Context, args []string) (subprocess.MCPCallResult, error) {
	bin, err := resolveTailscale()
	if err != nil {
		content, _ := json.Marshal("tailscale " + strings.Join(args, " ") + " failed: " + err.Error())
		return subprocess.MCPCallResult{IsError: true, Content: content}, nil
	}
	var out []byte
	// 'status' rarely needs sudo; changing the connection does.
	if args[0] == "status" {
		out, err = exec.CommandContext(ctx, bin, args...).CombinedOutput()
	} else {
		out, err = exec.CommandContext(ctx, "sudo", append([]string{bin}, args...)...).CombinedOutput()
	}

	var respText string
	if err != nil {
		respText = fmt.Sprintf("tailscale %v failed: %v\n%s", args, err, string(out))
	} else {
		respText = fmt.Sprintf("tailscale %v successful.\n%s", args, string(out))
	}

	content, _ := json.Marshal(respText)
	return subprocess.MCPCallResult{IsError: err != nil, Content: content}, nil
}
