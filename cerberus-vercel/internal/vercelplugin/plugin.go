package vercelplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

// Argument keys the host adds to a call itself, from --dry-run and --ack.
const (
	argDryRun       = "dry_run"
	argAcknowledged = "acknowledged"
)

// Plugin serves the Vercel deployment connector over the plugin-sdk
// subprocess protocol.
type Plugin struct {
	config subprocess.ConfigReader
	scrub  scrubber
	// findCLI is the test seam for resolving the Vercel CLI.
	findCLI func() string
}

var (
	_ subprocess.Plugin        = (*Plugin)(nil)
	_ subprocess.HealthChecker = (*Plugin)(nil)
	_ subprocess.MCPHandler    = (*Plugin)(nil)
)

// New builds the plugin. Secrets and the profiles_file field arrive in init
// config; nothing is resolved here.
func New() *Plugin { return &Plugin{findCLI: findVercelCLI} }

func (p *Plugin) Init(_ context.Context, params subprocess.InitParams) (subprocess.InitResult, error) {
	// ConfigReader rather than the raw map: its Secret() registers the value
	// with the SDK logger's redaction tracker as well.
	p.config = subprocess.NewConfigReader(params.Config)
	def := Definition()
	return subprocess.InitResult{
		ID:          def.ID,
		Name:        "Cerberus Vercel Connector",
		Version:     def.Version,
		Description: "Vercel deployments from operator-defined profiles, for Cerberus",
		Protocol:    subprocess.ProtocolVersion,
	}, nil
}

// Load never fails: a missing token is not an error (the Vercel CLI can use
// its own login session), and a missing profiles file is reported by status
// and refused by the operation that needed it.
func (p *Plugin) Load(context.Context) (subprocess.LoadResult, error) {
	p.scrub = newScrubber(p.token())
	return subprocess.LoadResult{}, nil
}

func (p *Plugin) Unload(context.Context) error { return nil }

// Health makes no network call and runs no command.
func (p *Plugin) Health(context.Context) (subprocess.HealthStatus, error) {
	status := p.status()
	if len(status.Problems) > 0 {
		return subprocess.HealthStatus{OK: false, Message: strings.Join(status.Problems, " ")}, nil
	}
	return subprocess.HealthStatus{OK: true, Message: fmt.Sprintf("%d deployment profiles", status.Profiles)}, nil
}

func (p *Plugin) secret(name, env string) string {
	if p.config != nil {
		if value := p.config.Secret(name); value != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(os.Getenv(env))
}

func (p *Plugin) token() string { return p.secret(SecretToken, TokenEnvVar) }
func (p *Plugin) scope() string { return p.secret(SecretScope, ScopeEnvVar) }

func (p *Plugin) profilesPath() string {
	if p.config == nil {
		return ""
	}
	return expandHome(strings.TrimSpace(p.config.String(FieldProfilesFile)))
}

func (p *Plugin) cli() string {
	if p.findCLI == nil {
		return findVercelCLI()
	}
	return p.findCLI()
}

func (p *Plugin) status() Status {
	status := Status{
		VercelCLI:       p.cli(),
		TokenConfigured: p.token() != "",
		ScopeConfigured: p.scope() != "",
		ProfilesFile:    p.profilesPath(),
		Problems:        []string{},
	}
	if status.VercelCLI == "" {
		status.Problems = append(status.Problems, errNoVercelCLI.Error())
	}
	profiles, err := loadProfiles(status.ProfilesFile)
	if err != nil {
		status.Problems = append(status.Problems, err.Error())
	} else {
		status.ProfilesFileRead = true
		status.Profiles = len(profiles)
	}
	return status
}

func (p *Plugin) MCPCallTool(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	result, err := p.call(ctx, req)
	if err == nil {
		result.Content = []byte(p.scrub.text(string(result.Content)))
		return withOperationTelemetry(req, result), nil
	}
	var coded *cerbplugin.CodedError
	if errors.As(err, &coded) {
		return cerbplugin.ErrorResult(coded.Code, p.scrub.text(err.Error())), nil
	}
	return result, p.scrub.err(err)
}

func (p *Plugin) call(ctx context.Context, req subprocess.MCPCallRequest) (subprocess.MCPCallResult, error) {
	op, ok := cerbplugin.OperationFromToolName(ConnectorID, req.ToolName, Manifest())
	if !ok {
		return subprocess.MCPCallResult{}, fmt.Errorf("unsupported tool %q", req.ToolName)
	}
	args := req.Arguments
	dryRun, _ := args[argDryRun].(bool)

	switch op.Name {
	case OpStatus:
		if dryRun {
			return subprocess.MCPCallResult{}, readOnlyRefusal(op.Name)
		}
		return marshalResult(p.status())

	case OpListProfiles:
		if dryRun {
			return subprocess.MCPCallResult{}, readOnlyRefusal(op.Name)
		}
		profiles, err := loadProfiles(p.profilesPath())
		if err != nil {
			return subprocess.MCPCallResult{}, invalid(err)
		}
		out := []ProfileSummary{}
		for _, profile := range profiles {
			out = append(out, profile.summary())
		}
		return marshalResult(out)

	case OpDeploy:
		// The host refuses an unacknowledged exec before the call reaches
		// this process. The check is here for the binary driven any other
		// way, so the plugin's own contract is "no unacknowledged deploy".
		if acked, _ := args[argAcknowledged].(bool); !dryRun && !acked {
			return subprocess.MCPCallResult{}, invalid(errors.New("deploy runs the profile's commands and deploys it, and requires acknowledgment. " +
				"Re-run it acknowledged (--ack), or preview it first with --dry-run"))
		}
		id, _ := args["profile"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			return subprocess.MCPCallResult{}, invalid(errors.New("invalid arguments: profile is required"))
		}
		profiles, err := loadProfiles(p.profilesPath())
		if err != nil {
			return subprocess.MCPCallResult{}, invalid(err)
		}
		profile, ok := findProfile(profiles, id)
		if !ok {
			return subprocess.MCPCallResult{}, invalid(fmt.Errorf("no profile %q in %s; list them with `cerberus connectors exec vercel list_profiles`", id, p.profilesPath()))
		}
		planned, err := planDeploy(profile, p.token(), p.scope(), p.cli())
		if err != nil {
			return subprocess.MCPCallResult{}, invalid(err)
		}
		if dryRun {
			return marshalResult(preview(ctx, planned))
		}
		return marshalResult(planned.run(ctx))

	default:
		return subprocess.MCPCallResult{}, fmt.Errorf("operation %q is declared but not served", op.Name)
	}
}

func preview(ctx context.Context, planned plan) DryRunPreview {
	profile := planned.profile
	return DryRunPreview{
		DryRun:    true,
		Connector: ConnectorID,
		Operation: OpDeploy,
		Summary:   fmt.Sprintf("Would run %d steps of profile %s in %s.", len(planned.steps), profile.ID, profile.RepoPath),
		Target:    map[string]any{"profile": profile.ID},
		Input:     map[string]any{"profile": profile.ID},
		Warnings: []string{
			"Every step runs as a shell command or the Vercel CLI on this machine, in the profile's repo_path.",
			"The run plans again when it starts. The host's approval binds this preview, so a profile or checkout that changed since makes the approval stale.",
		},
		RepoPath:      profile.RepoPath,
		ProfileSHA256: profile.digest(),
		Steps:         planned.planned(),
		Git:           inspectGit(ctx, profile),
	}
}

func readOnlyRefusal(op string) error {
	return invalid(fmt.Errorf("%s is read-only and has no dry-run preview; run it without --dry-run", op))
}

// invalid codes this plugin's refusal of the call as the caller's to fix.
func invalid(err error) error {
	return cerbplugin.WithCode(cerbplugin.ErrorInvalidArgs, err)
}

func marshalResult[T any](data T) (subprocess.MCPCallResult, error) {
	content, err := json.Marshal(data)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{Content: content}, nil
}
