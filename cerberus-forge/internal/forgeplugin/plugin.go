package forgeplugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// errMissingCredential is what every Forge call returns when no token arrived.
// It is short on purpose: the host recognises a failure from a plugin that
// loaded without a required secret and appends its own guidance naming every
// way to supply one, so repeating it here would print it twice.
var errMissingCredential = errors.New("no Laravel Forge API token was supplied to this plugin")

// credentialGuidance is the full recovery instruction, reported by Health,
// which the host passes through without adding its own. It is worded to
// survive the host's redact.Text: no "name: value" or "name=value" shapes, no
// flag followed by a word (see redaction_test.go).
var credentialGuidance = errMissingCredential.Error() + ". Supply it as " + TokenEnvVar +
	", as the api_token entry under forge in connector-secrets.yaml, or as keychain://forge/api_token, " +
	"then reload the plugin with `cerberus connectors plugin managed load forge`"

// Plugin serves the Laravel Forge connector over the plugin-sdk subprocess
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

// New builds the plugin against the live Forge API. The token is not looked up
// here: Cerberus resolves every secret this manifest declares and hands the
// values over in init config.
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
		Name:        "Cerberus Laravel Forge Connector",
		Version:     def.Version,
		Description: "Laravel Forge server and site administration for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails on a missing token. The plugin starts, reports the gap in
// Health, and each Forge call fails with errMissingCredential. The deploy and
// command previews need no credential and keep working; the deployment
// script preview reads the current script, so it needs one.
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
		if token := p.config.Secret(SecretAPIToken); token != "" {
			return token
		}
	}
	return os.Getenv(TokenEnvVar)
}

// writeOperations change a site, and so need acknowledgment. All three have a
// dry-run preview. The contract says the same thing, and
// TestGatesMatchTheContract holds the two in step.
var writeOperations = map[string]bool{
	"update_deployment_script": true,
	"deploy_site":              true,
	"exec_site_command":        true,
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
// token Forge rejects (401) are credential_missing; an API that cannot be
// reached at all is unavailable; arguments this plugin refused, and a server
// or site id Forge does not know (404), are invalid_args. Anything else Forge
// answered, a 403 included, stays uncoded.
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

	dryRun := false
	if writeOperations[op.Name] {
		var err error
		if dryRun, err = p.writeMode(args, op.Name); err != nil {
			return subprocess.MCPCallResult{}, err
		}
	} else if args.boolean(argDryRun) {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs,
			fmt.Errorf("%s is read-only and has no dry-run preview; run it without --dry-run", op.Name))
	}

	switch op.Name {
	case "list_servers":
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ListServers(ctx))

	case "get_server", "list_sites":
		serverID := args.positiveID("server_id")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if op.Name == "get_server" {
			return marshalResult(p.backend.GetServer(ctx, serverID))
		}
		return marshalResult(p.backend.ListSites(ctx, serverID))

	case "get_deployment_script":
		serverID, siteID := args.serverSite()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.GetDeploymentScript(ctx, serverID, siteID))

	case "update_deployment_script":
		serverID, siteID := args.serverSite()
		content := args.requiredRaw("content")
		autoSource := args.boolean("auto_source")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		target := map[string]any{"server_id": serverID, "site_id": siteID}
		if dryRun {
			// The preview reads the current script: a real Forge call. If
			// it fails the dry run fails, rather than showing a diff against
			// nothing.
			current, err := p.backend.GetDeploymentScript(ctx, serverID, siteID)
			if err != nil {
				return subprocess.MCPCallResult{}, err
			}
			diff := diffScripts(current, content)
			diff.Unified = p.scrub.text(diff.Unified)
			preview := newPreview(op.Name, "Would replace a Forge site's deployment script.", target,
				map[string]any{"content": contentDigest(content), "auto_source": autoSource},
				"The deployment script is what deploy_site runs next.",
				"The diff shows changed script lines, which can carry secrets.",
				"auto_source is not compared: Forge returns only the script text.")
			preview.Diff = &diff
			return marshalResult(preview, nil)
		}
		if err := p.backend.UpdateDeploymentScript(ctx, serverID, siteID, content, autoSource); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(SiteAction{ServerID: serverID, SiteID: siteID, Action: "update_deployment_script"}, nil)

	case "deploy_site":
		serverID, siteID := args.serverSite()
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would trigger a Forge site deployment.",
				map[string]any{"server_id": serverID, "site_id": siteID}, nil), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if err := p.backend.DeploySite(ctx, serverID, siteID); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(SiteAction{ServerID: serverID, SiteID: siteID, Action: "deploy"}, nil)

	case "exec_site_command":
		serverID, siteID := args.serverSite()
		command := args.requiredRaw("command")
		if err := invalid(args.err()); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		if dryRun {
			return marshalResult(newPreview(op.Name, "Would execute a Forge site command.",
				map[string]any{"server_id": serverID, "site_id": siteID},
				map[string]any{"command": command}), nil)
		}
		if err := p.ready(); err != nil {
			return subprocess.MCPCallResult{}, err
		}
		return marshalResult(p.backend.ExecuteSiteCommand(ctx, serverID, siteID, command))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

// contentDigest describes a proposed script by size and hash. The diff shows
// the lines that change; the digest confirms which script would be written
// without repeating all of it.
func contentDigest(content string) map[string]any {
	sum := sha256.Sum256([]byte(content))
	return map[string]any{"bytes": len(content), "sha256": hex.EncodeToString(sum[:])}
}

// writeMode reads the dry-run flag for a write and refuses a real write that
// arrives unacknowledged.
//
// The host already refuses an acknowledgment-gated operation without --ack
// before the call reaches this process, so under the host this check never
// fires. It is here for the binary driven any other way, so that the plugin's
// own contract is "no unacknowledged write" rather than "no unacknowledged
// write provided the caller checked". A dry run changes nothing and needs no
// acknowledgment.
func (p *Plugin) writeMode(args *argMap, operation string) (bool, error) {
	dryRun := args.boolean(argDryRun)
	if !dryRun && !args.boolean(argAcknowledged) {
		return false, fmt.Errorf("%s changes a Forge site and requires acknowledgment. Re-run it acknowledged (--ack), or preview it first with --dry-run", operation)
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
