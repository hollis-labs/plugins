package digitaloceanplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"

	"github.com/digitalocean/godo"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/cerberus/pkg/resource"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// errMissingCredential is what every DigitalOcean call returns when no token
// arrived. It is short on purpose: the host recognises a failure from a plugin
// that loaded without a required secret and appends its own guidance naming
// every way to supply one, so repeating it here would print it twice.
var errMissingCredential = errors.New("no DigitalOcean API token was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes, no
// flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply it as " + TokenEnvVar +
	", as the api_token entry under digitalocean in connector-secrets.yaml, or as keychain://digitalocean/api_token, " +
	"then reload the plugin with `cerberus connectors plugin managed load digitalocean`"

// Plugin serves the DigitalOcean connector over the plugin-sdk subprocess
// protocol.
type Plugin struct {
	newBackend func(apiToken string) Backend
	config     subprocess.ConfigReader
	backend    Backend
	scrub      scrubber
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin against the live DigitalOcean API. The token is not
// looked up here: Cerberus resolves every secret this manifest declares and
// hands the values over in init config.
func New() *Plugin { return &Plugin{newBackend: NewSDKBackend} }

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
		Name:        "Cerberus DigitalOcean Connector",
		Version:     def.Version,
		Description: "DigitalOcean droplet administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing token. The plugin starts, reports the gap in
// Health, and each DigitalOcean call fails with errMissingCredential. Dry runs
// need no credential and keep working.
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

// Health makes no network call: it reports whether the plugin can reach
// DigitalOcean at all, which is whether a credential arrived.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	if p.backend == nil {
		return subprocess.HealthStatus{OK: false, Message: credentialGuidance}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "API token configured"}, nil
}

// token reads the credential the host resolved, falling back to the
// environment only for a binary run directly, outside the host, which receives
// no init config.
func (p *Plugin) token() string {
	if p.config != nil {
		if token := p.config.Secret(SecretAPIToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

// ackOperations are the operations that change DigitalOcean, and so need
// acknowledgment; previewOperations are the ones among them with a dry-run
// preview. `start` is acknowledged but has no preview. The contract says the
// same thing, and TestGatesMatchTheContract holds the two in step.
var (
	ackOperations = map[string]bool{
		"create_droplet": true,
		"start":          true,
		"stop":           true,
		"destroy":        true,
	}
	previewOperations = map[string]bool{
		"create_droplet": true,
		"stop":           true,
		"destroy":        true,
	}
)

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		return withOperationTelemetry(req, result), nil
	}
	// The code is read before scrubbing, which drops the chain; the message
	// that carries it is scrubbed like any other.
	if code := errorCode(err); code != "" {
		return cerbplugin.ErrorResult(code, p.scrub.text(err.Error())), nil
	}
	return result, p.scrub.err(err)
}

// errorCode is the Cerberus code for a failed call. A missing token and a
// token DigitalOcean rejects (401) are credential_missing; an API that cannot
// be reached at all is unavailable; arguments this plugin refused, and a
// droplet id DigitalOcean does not know (404), are invalid_args. Anything else
// the API answered, a 403 included, stays uncoded.
func errorCode(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	var apiErr *godo.ErrorResponse
	switch {
	case errors.As(err, &coded):
		return coded.Code
	case errors.Is(err, errMissingCredential):
		return cerbplugin.ErrorCredentialMissing
	case errors.As(err, &apiErr) && apiErr.Response != nil:
		switch apiErr.Response.StatusCode {
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

	dryRun := false
	if args.boolean(argDryRun) && !previewOperations[op.Name] {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s has no dry-run preview; run it without --dry-run", op.Name))
	}
	if ackOperations[op.Name] {
		var err error
		if dryRun, err = p.writeMode(args, op.Name); err != nil {
			return subprocess.MCPCallResult{}, err
		}
	}

	switch op.Name {
	case "list_droplets":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListDroplets(ctx))

	case "get_droplet":
		id := args.dropletID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetDroplet(ctx, id))

	case "create_droplet":
		req := CreateRequest{
			Name:     args.required("name"),
			Region:   args.required("region"),
			Size:     args.required("size"),
			Image:    args.required("image"),
			SSHKeys:  args.strSlice("ssh_keys"),
			UserData: args.rawString("user_data"),
		}
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would create a DigitalOcean droplet.",
				map[string]any{"name": req.Name, "region": req.Region, "size": req.Size, "image": req.Image},
				map[string]any{"ssh_keys": req.SSHKeys, "user_data": userDataDigest(req.UserData)}), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		id, err := p.backend.CreateDroplet(ctx, req)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		droplet, err := p.backend.GetDroplet(ctx, id)
		if err != nil {
			// Created, billable, and not to be retried: report the id.
			return marshalResult(CreatedReference{DropletID: id}, nil)
		}
		return marshalResult(droplet, nil)

	case "start":
		id := args.dropletID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.backend.PowerOn(ctx, id); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(DropletAction{DropletID: id, Action: "power_on"}, nil)

	case "stop", "destroy":
		id := args.dropletID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		summary, action := "Would power off a DigitalOcean droplet.", "power_off"
		if op.Name == "destroy" {
			summary, action = "Would destroy a DigitalOcean droplet.", "destroy"
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, summary, map[string]any{"droplet_id": id}, nil), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		call := p.backend.PowerOff
		if op.Name == "destroy" {
			call = p.backend.Delete
		}
		if err := call(ctx, id); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(DropletAction{DropletID: id, Action: action}, nil)

	case "status":
		id := args.dropletID()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		droplet, err := p.backend.GetDroplet(ctx, id)
		if err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(string(dropletState(droplet.Status)), nil)

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

// dropletState normalizes a droplet status to a Cerberus resource state,
// exactly as the compiled-in connector did.
func dropletState(status string) resource.State {
	switch status {
	case "active":
		return resource.StateRunning
	case "off":
		return resource.StateStopped
	case "new":
		return resource.StateStarting
	case "archive":
		return resource.StateDestroyed
	default:
		return resource.StateUnknown
	}
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses an acknowledgment-gated operation without --ack
// before the call reaches this process, so under the host this check never
// fires. It is
// here for the binary driven any other way, so that the plugin's own contract
// is "no unacknowledged write" rather than "no unacknowledged write provided
// the caller checked". A dry run changes nothing and needs no acknowledgment.
func (p *Plugin) writeMode(args *argMap, operation string) (bool, error) {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		return false, fmt.Errorf("%s changes DigitalOcean and requires acknowledgment. Re-run it acknowledged (--ack)%s", operation, previewHint(operation))
	}
	return dryRun, nil
}

func previewHint(operation string) string {
	if previewOperations[operation] {
		return ", or preview it first with --dry-run"
	}
	return ""
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
