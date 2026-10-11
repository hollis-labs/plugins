package caddyplugin

import (
	"context"
	"errors"
	"fmt"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Plugin serves the Caddy connector over the plugin-sdk subprocess protocol.
type Plugin struct {
	config subprocess.ConfigReader
	// resolve and run are the test seams; production uses the per-call binary
	// resolver and os/exec.
	resolve func() (string, error)
	run     runner
	token   string
	scrub   scrubber
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

func New() *Plugin { return &Plugin{run: execRunner{}} }

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: Secret() also registers the value
	// with the SDK logger's redaction tracker.
	p.config = subprocess.NewConfigReader(params.Config)
	p.token = p.config.Secret(SecretCloudflareToken)
	p.scrub = newScrubber(p.token)
	return subprocess.InitResult{ID: ConnectorID, Name: "Cerberus Caddy Connector", Version: Version, Protocol: subprocess.ProtocolVersion}, nil
}

func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error {
	p.token = ""
	p.scrub = scrubber{}
	return nil
}

// caddyBinary resolves the caddy binary for this call.
func (p *Plugin) caddyBinary() (string, error) {
	if p.resolve != nil {
		return p.resolve()
	}
	configured := ""
	if p.config != nil {
		configured = p.config.String(ConfigCaddyPath)
	}
	return newBinaryResolver(configured).resolve()
}

func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	bin, err := p.caddyBinary()
	if err != nil {
		return subprocess.HealthStatus{OK: false, Message: err.Error()}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: "caddy at " + bin}, nil
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unknown tool %q", req.ToolName)
	}
	args := req.Arguments
	if args == nil {
		args = map[string]any{}
	}

	var result subprocess.MCPCallResult
	var err error
	switch op.Name {
	case "reload":
		result, err = p.reload(ctx, args)
	case "validate":
		result, err = p.validate(ctx, args)
	case "render":
		result, err = p.render(args)
	case "apply":
		result, err = p.apply(ctx, args)
	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("unimplemented operation: %s", op.Name)
	}
	if err != nil {
		// Coded errors keep their code; the message is scrubbed either way.
		if code := codeOf(err); code != "" {
			return cerbplugin.ErrorResult(code, p.scrub.text(err.Error())), nil
		}
		return result, fmt.Errorf("%s", p.scrub.text(err.Error()))
	}
	return result, nil
}

func codeOf(err error) cerbplugin.ErrorCode {
	var coded *cerbplugin.CodedError
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// caddyCommand runs a caddy subcommand with the binary resolved for this call.
// A caddy that cannot be found is connector_unavailable.
func (p *Plugin) caddyCommand(ctx context.Context, args []string, env []string) ([]byte, error) {
	bin, err := p.caddyBinary()
	if err != nil {
		return nil, cerbplugin.WithCode(cerbplugin.ErrorUnavailable, err)
	}
	return p.run.Run(ctx, bin, args, env)
}

func (p *Plugin) reload(ctx context.Context, args map[string]any) (subprocess.MCPCallResult, error) {
	cmdArgs := []string{"reload"}
	if cp, ok := args["config_path"].(string); ok && cp != "" {
		cmdArgs = append(cmdArgs, "--config", cp)
	}
	out, err := p.caddyCommand(ctx, cmdArgs, nil)
	if isCoded(err) {
		return subprocess.MCPCallResult{}, err
	}
	if err != nil {
		return jsonResult(fmt.Sprintf("caddy reload failed: %v\n%s", err, p.scrub.text(string(out))), true)
	}
	return jsonResult("Caddy reloaded successfully.\n"+p.scrub.text(string(out)), false)
}

func (p *Plugin) validate(ctx context.Context, args map[string]any) (subprocess.MCPCallResult, error) {
	cmdArgs := []string{"validate"}
	if cp, ok := args["config_path"].(string); ok && cp != "" {
		cmdArgs = append(cmdArgs, "--config", cp)
	}
	out, err := p.caddyCommand(ctx, cmdArgs, p.tokenEnv())
	if isCoded(err) {
		return subprocess.MCPCallResult{}, err
	}
	if err != nil {
		return jsonResult(fmt.Sprintf("caddy validate failed: %v\n%s", err, p.scrub.text(string(out))), true)
	}
	return jsonResult("Caddyfile is valid.\n"+p.scrub.text(string(out)), false)
}

// tokenEnv is the environment a validate runs with: the DNS-01 token under the
// default placeholder name, so a Caddyfile that references it provisions. It
// reaches only the short-lived caddy child, never a file.
func (p *Plugin) tokenEnv() []string {
	if p.token == "" {
		return nil
	}
	return []string{DefaultTokenEnv + "=" + p.token}
}
