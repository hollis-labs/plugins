package vercelplugin

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// tokenDisplay is how a step shows the token it is handed. The placeholder is
// a name, never a value, and it must survive the host's redact.Text:
// "--token [vercel token]" did not, because the flag rule read "[vercel" as
// the token and the plan came back as "[REDACTED] token]".
const tokenDisplay = "VERCEL_TOKEN=<vercel token> " //nolint:gosec // the name shown in place of the token, never a value

// maxStepOutput is how much of a step's output a result keeps: its tail,
// where a build's error and the CLI's deployment URL are. The host caps a
// whole result at 1 MiB by default.
const maxStepOutput = 64 << 10

// step is a planned step with what actually runs: a shell command or an argv,
// plus environment added to the child's. A credential travels only in env:
// argv and a shell string are visible to every local user through ps, and a
// login shell's profile tracing would echo it.
type step struct {
	name    string
	display string
	shell   string
	argv    []string
	env     []string
	// deploy marks the step whose output names the deployment URL.
	deploy bool
}

type plan struct {
	profile Profile
	steps   []step
}

func (p plan) planned() []PlannedStep {
	out := []PlannedStep{}
	for _, s := range p.steps {
		out = append(out, PlannedStep{Name: s.name, Command: s.display, Env: envNames(s.env)})
	}
	return out
}

func envNames(env []string) []string {
	var names []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	return names
}

// planDeploy computes the steps a deploy of profile runs, without running any
// of them. token and defaultScope are the resolved secrets. The token is
// required and reaches every CLI step as VERCEL_TOKEN; its value stays out of
// every displayed command. globalConfig is the isolated directory the CLI is
// pointed at with --global-config, so it never reads the operator's own
// `vercel login` session.
func planDeploy(profile Profile, token, defaultScope, vercelCLI, globalConfig string) (plan, error) {
	out := plan{profile: profile}
	if token == "" {
		return out, errNoToken
	}
	if provider := strings.TrimSpace(profile.Provider); provider != "" && provider != ConnectorID {
		return out, fmt.Errorf("profile %q is for provider %q, and this plugin deploys only to vercel", profile.ID, provider)
	}
	if profile.RepoPath == "" {
		return out, fmt.Errorf("profile %q has no repo_path", profile.ID)
	}
	if info, err := os.Stat(profile.RepoPath); err != nil || !info.IsDir() {
		return out, fmt.Errorf("repo path %q of profile %q is not a directory on this machine", profile.RepoPath, profile.ID)
	}
	tokenEnv := []string{"VERCEL_TOKEN=" + token}
	scope := strings.TrimSpace(profile.VercelScope)
	if scope == "" {
		scope = defaultScope
	}
	if profile.PreflightCommand != "" {
		out.steps = append(out.steps, step{name: "preflight", display: profile.PreflightCommand, shell: profile.PreflightCommand})
	}
	if profile.BuildCommand != "" {
		out.steps = append(out.steps, step{name: "build", display: profile.BuildCommand, shell: profile.BuildCommand})
	}
	if !hasVercelLink(profile.RepoPath) {
		if profile.VercelProject == "" {
			return out, fmt.Errorf("profile %q needs vercel_project before its first deploy, because its repo is not linked", profile.ID)
		}
		if vercelCLI == "" {
			return out, errNoVercelCLI
		}
		args := []string{"link", "--yes", "--project", profile.VercelProject}
		if scope != "" {
			args = append(args, "--scope", scope)
		}
		args = append(args, "--global-config", globalConfig)
		display := tokenDisplay + "vercel " + strings.Join(args, " ")
		out.steps = append(out.steps, step{name: "link", display: display, argv: append([]string{vercelCLI}, args...), env: tokenEnv})
	}
	command := strings.TrimSpace(profile.DeployCommand)
	if command == "" {
		command = "vercel --prod --yes"
	}
	display, shell := command, command
	if scope != "" && !strings.Contains(command, "--scope") {
		display += " --scope " + shellQuote(scope)
		shell += " --scope " + shellQuote(scope)
	}
	// A command that runs the CLI directly is pointed at the isolated
	// config too. A script the operator wrote gets VERCEL_TOKEN, which the
	// CLI prefers over a login session, and should pass it on.
	if isCLICommand(command) && !strings.Contains(command, "--global-config") {
		display += " --global-config " + shellQuote(globalConfig)
		shell += " --global-config " + shellQuote(globalConfig)
	}
	display = tokenDisplay + display
	out.steps = append(out.steps, step{name: "deploy", display: display, shell: shell, env: tokenEnv, deploy: true})
	return out, nil
}

// run executes the plan's steps in order, stopping at the first that fails.
// Each step's recorded command is the displayed one.
func (p plan) run(ctx context.Context) RunResult {
	result := RunResult{Profile: p.profile.ID, Steps: []StepRun{}, Git: inspectGit(ctx, p.profile)}
	for _, s := range p.steps {
		var cmd *exec.Cmd
		if s.shell != "" {
			cmd = exec.CommandContext(ctx, "/bin/sh", "-lc", s.shell) //nolint:gosec // the operator's own profile, confirmed against its plan
		} else {
			cmd = exec.CommandContext(ctx, s.argv[0], s.argv[1:]...) //nolint:gosec // as above
		}
		cmd.Dir = p.profile.RepoPath
		if len(s.env) > 0 {
			cmd.Env = append(os.Environ(), s.env...)
		}
		killGroupOnCancel(cmd)
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		err := cmd.Run()
		output, truncated := tail(combined.String(), maxStepOutput)
		record := StepRun{Name: s.name, Command: s.display, Success: err == nil, Output: output, OutputTruncated: truncated}
		if err != nil {
			record.Error = err.Error()
			result.Steps = append(result.Steps, record)
			result.Error = fmt.Sprintf("%s failed (%v)", s.name, err)
			return result
		}
		result.Steps = append(result.Steps, record)
		if s.deploy {
			result.DeploymentURL = extractDeploymentURL(combined.String())
		}
	}
	result.Success = true
	return result
}

// killGroupOnCancel runs the step in its own process group and kills the
// whole group when the call is cancelled, so a build's children do not
// outlive a deadline the host enforced.
func killGroupOnCancel(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}

// isCLICommand reports a deploy command that invokes the Vercel CLI itself.
func isCLICommand(command string) bool {
	return command == "vercel" || strings.HasPrefix(command, "vercel ")
}

// isolatedGlobalConfig is the directory the Vercel CLI is pointed at in place
// of its global config (~/.local/share/com.vercel.cli, where `vercel login`
// keeps its session). It is stable, so a plan and the run it approves show
// the same command, and it is ours: nothing logs in there.
func isolatedGlobalConfig() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find a cache directory for the Vercel CLI's isolated config (%w)", err)
	}
	dir := filepath.Join(base, "cerberus-vercel-plugin", "cli-config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create the Vercel CLI's isolated config %s (%w)", dir, err)
	}
	return dir, nil
}

func tail(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[len(s)-max:], true
}

func inspectGit(ctx context.Context, profile Profile) GitStatus {
	status := GitStatus{}
	status.Branch = strings.TrimSpace(gitOutput(ctx, profile.RepoPath, "rev-parse", "--abbrev-ref", "HEAD"))
	status.Commit = strings.TrimSpace(gitOutput(ctx, profile.RepoPath, "rev-parse", "HEAD"))
	status.Dirty = strings.TrimSpace(gitOutput(ctx, profile.RepoPath, "status", "--porcelain")) != ""
	if remote := strings.TrimSpace(profile.GitRemote); remote != "" {
		status.RemoteURL = strings.TrimSpace(gitOutput(ctx, profile.RepoPath, "remote", "get-url", remote))
	}
	if remotes := strings.TrimSpace(gitOutput(ctx, profile.RepoPath, "remote")); remotes != "" {
		status.Remotes = strings.Split(remotes, "\n")
	}
	return status
}

// gitOutput runs git in dir with the caller's git environment removed, so a
// GIT_DIR inherited from a hook cannot point it at another repository.
func gitOutput(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed git subcommands
	var env []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			env = append(env, entry)
		}
	}
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// errNoVercelCLI is the refusal when the Vercel CLI is not found. The daemon
// runs with launchd's minimal PATH unless its plist carries one, so the
// search does not trust PATH alone.
var errNoVercelCLI = fmt.Errorf("the Vercel CLI was not found on PATH or in %s. Install it (npm install --global vercel) and deploy again", strings.Join(vercelSearchDirs(), ", "))

// vercelSearchDirs are where a global npm, pnpm, bun or Homebrew install puts
// the CLI, beyond PATH.
func vercelSearchDirs() []string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs,
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, "Library", "pnpm"),
			filepath.Join(home, ".local", "share", "pnpm"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".volta", "bin"),
		)
	}
	return dirs
}

// findVercelCLI resolves the CLI per call, never once at startup: a CLI
// installed after the plugin loaded is found on the next call.
func findVercelCLI() string {
	if path, err := exec.LookPath("vercel"); err == nil {
		return path
	}
	for _, dir := range vercelSearchDirs() {
		candidate := filepath.Join(dir, "vercel")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

var (
	ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	urlPattern        = regexp.MustCompile(`https?://[^\s]+`)
)

// extractDeploymentURL finds the deployment's URL in the deploy step's
// output: a line mentioning production first, then any URL that is not the
// Vercel dashboard, then the dashboard itself.
func extractDeploymentURL(output string) string {
	lines := strings.Split(output, "\n")
	pass := func(match func(line string) string) string {
		for i := len(lines) - 1; i >= 0; i-- {
			line := strings.TrimSpace(ansiEscapePattern.ReplaceAllString(lines[i], ""))
			if line == "" {
				continue
			}
			if candidate := match(line); candidate != "" {
				return candidate
			}
		}
		return ""
	}
	if u := pass(func(line string) string {
		if !strings.Contains(strings.ToLower(line), "production") {
			return ""
		}
		return preferredURL(line, true)
	}); u != "" {
		return u
	}
	if u := pass(func(line string) string { return preferredURL(line, false) }); u != "" {
		return u
	}
	return pass(func(line string) string { return preferredURL(line, true) })
}

func preferredURL(line string, allowDashboard bool) string {
	for _, match := range urlPattern.FindAllString(line, -1) {
		parsed, err := url.Parse(match)
		if err != nil || parsed.Host == "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if (host == "vercel.com" || strings.HasSuffix(host, ".vercel.com")) && !allowDashboard {
			continue
		}
		return match
	}
	return ""
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
