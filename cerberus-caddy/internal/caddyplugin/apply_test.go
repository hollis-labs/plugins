package caddyplugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const previousConfig = "# previous\nexample.test {\n\trespond \"old\"\n}\n"

func withAck(args map[string]any) map[string]any {
	args["acknowledged"] = true
	return args
}

func TestApplyRefusesWithoutAcknowledgment(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	res, err := call(t, p, "apply", baseArgs(path))
	if err == nil && !res.IsError {
		t.Fatalf("an unacknowledged apply ran: %s", res.Content)
	}
	if len(run.calls) != 0 || readFile(t, path) != previousConfig {
		t.Fatal("an unacknowledged apply had an effect")
	}
}

func TestApplyDryRunPreviewsTheDiffAndChangesNothing(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	args := baseArgs(path)
	args["dry_run"] = true
	res, err := call(t, p, "apply", args)
	if err != nil || res.IsError {
		t.Fatalf("dry run: %v %s", err, res.Content)
	}
	out := decode[ApplyResult](t, res)
	if !out.DryRun || !out.Changed || out.Applied || out.Reloaded {
		t.Fatalf("result = %+v", out)
	}
	if !strings.Contains(out.Diff, "-# previous") || !strings.Contains(out.Diff, "+\tdefault_bind 100.64.0.10") {
		t.Errorf("diff is not a real preview:\n%s", out.Diff)
	}
	if len(run.calls) != 0 {
		t.Errorf("a dry run ran caddy: %+v", run.calls)
	}
	if readFile(t, path) != previousConfig {
		t.Error("a dry run wrote the file")
	}
	if len(strayTemps(t, path)) != 0 {
		t.Error("a dry run left a staging file")
	}
	assertNoToken(t, "dry run result", string(res.Content))
}

func TestApplyWritesValidatesThenReloads(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || res.IsError {
		t.Fatalf("apply: %v %s", err, res.Content)
	}
	out := decode[ApplyResult](t, res)
	if !out.Applied || !out.Validated || !out.Reloaded || out.RolledBack || !out.Changed {
		t.Fatalf("result = %+v", out)
	}
	if got := strings.Join(run.subcommands(), ","); got != "validate,reload" {
		t.Fatalf("order = %s, want validate,reload", got)
	}
	want := renderCaddyfile(mustParse(t, baseArgs(path)))
	if readFile(t, path) != want {
		t.Errorf("file content is not the rendered Caddyfile")
	}
	// Validate saw the candidate, not the live file, and not previous content.
	if v := run.calls[0]; v.config != want || strings.Contains(strings.Join(v.args, " "), path+" ") {
		t.Errorf("validate call = %+v", v)
	}
	if strings.Join(run.calls[1].args, " ") != "reload --adapter caddyfile --config "+path {
		t.Errorf("reload args = %v", run.calls[1].args)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the previous file's 0640", info.Mode().Perm())
	}
	if len(strayTemps(t, path)) != 0 {
		t.Errorf("staging files left behind: %v", strayTemps(t, path))
	}
}

func TestApplyTokenReachesOnlyTheValidateChildEnvironment(t *testing.T) {
	path := tempConfig(t, "")
	run := &fakeRunner{}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || res.IsError {
		t.Fatalf("apply: %v %s", err, res.Content)
	}
	if len(run.calls[0].env) != 1 || run.calls[0].env[0] != DefaultTokenEnv+"="+sentinelToken {
		t.Errorf("validate env = %v", run.calls[0].env)
	}
	if len(run.calls[1].env) != 0 {
		t.Errorf("reload env = %v; the token has no business there", run.calls[1].env)
	}
	for _, c := range run.calls {
		assertNoToken(t, "caddy args", strings.Join(c.args, " "))
	}
	assertNoToken(t, "applied file", readFile(t, path))
	assertNoToken(t, "result", string(res.Content))
}

func TestApplyWithoutATokenWarnsAndStillWorks(t *testing.T) {
	path := tempConfig(t, "")
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	res, _ := call(t, p, "apply", withAck(baseArgs(path)))
	out := decode[ApplyResult](t, res)
	if !out.Applied || len(out.Warnings) < 2 {
		t.Fatalf("result = %+v", out)
	}
	if len(run.calls[0].env) != 0 {
		t.Errorf("env = %v", run.calls[0].env)
	}
}

func TestApplyInvalidCandidateNeverReplacesTheLiveFile(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{reply: func(c fakeCall) ([]byte, error) {
		return []byte("adapting config: unrecognized directive; token " + sentinelToken), errors.New("exit status 1")
	}}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || !res.IsError {
		t.Fatalf("want an error result, got %v %s", err, res.Content)
	}
	out := decode[ApplyResult](t, res)
	if out.Applied || out.Validated || out.Reloaded {
		t.Fatalf("result = %+v", out)
	}
	if !strings.Contains(out.CaddyOutput, "unrecognized directive") {
		t.Errorf("caddy's output was not reported: %q", out.CaddyOutput)
	}
	assertNoToken(t, "result", string(res.Content))
	if got := strings.Join(run.subcommands(), ","); got != "validate" {
		t.Errorf("calls = %s; reload must not run after a failed validate", got)
	}
	if readFile(t, path) != previousConfig {
		t.Error("the live file changed")
	}
	if len(strayTemps(t, path)) != 0 {
		t.Error("staging file left behind")
	}
}

func TestApplyReloadFailureRestoresThePreviousFile(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{reply: func(c fakeCall) ([]byte, error) {
		if c.args[0] == "reload" {
			return []byte("reload: loading new config: tls: " + sentinelToken), errors.New("exit status 1")
		}
		return nil, nil
	}}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || !res.IsError {
		t.Fatalf("want an error result, got %v %s", err, res.Content)
	}
	out := decode[ApplyResult](t, res)
	if out.Applied || !out.Validated || out.Reloaded || !out.RolledBack {
		t.Fatalf("result = %+v", out)
	}
	if readFile(t, path) != previousConfig {
		t.Errorf("previous file not restored:\n%s", readFile(t, path))
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("restored mode = %v", info.Mode().Perm())
	}
	assertNoToken(t, "result", string(res.Content))
	if len(strayTemps(t, path)) != 0 {
		t.Error("staging file left behind")
	}
}

func TestApplyReloadFailureWithNoPreviousFileRemovesTheNewOne(t *testing.T) {
	path := tempConfig(t, "")
	run := &fakeRunner{reply: func(c fakeCall) ([]byte, error) {
		if c.args[0] == "reload" {
			return []byte("boom"), errors.New("exit status 1")
		}
		return nil, nil
	}}
	p := newTestPlugin(t, run, nil)
	res, _ := call(t, p, "apply", withAck(baseArgs(path)))
	out := decode[ApplyResult](t, res)
	if !res.IsError || !out.RolledBack {
		t.Fatalf("result = %+v", out)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the failed first config is still on disk: %v", err)
	}
}

func TestApplyUnchangedDoesNotTouchCaddy(t *testing.T) {
	path := tempConfig(t, "")
	if err := os.WriteFile(path, []byte(renderCaddyfile(mustParse(t, baseArgs(path)))), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || res.IsError {
		t.Fatalf("apply: %v %s", err, res.Content)
	}
	out := decode[ApplyResult](t, res)
	if out.Changed || out.Applied || len(run.calls) != 0 {
		t.Fatalf("result = %+v, calls = %v", out, run.calls)
	}
}

func TestApplyWithoutCaddyIsUnavailableAndLeavesTheFile(t *testing.T) {
	path := tempConfig(t, previousConfig)
	p := newTestPlugin(t, &fakeRunner{}, nil)
	p.resolve = func() (string, error) { return "", errCaddyNotFound }
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || !res.IsError || !strings.Contains(string(res.Content), "connector_unavailable") {
		t.Fatalf("result = %v %s", err, res.Content)
	}
	if readFile(t, path) != previousConfig || len(strayTemps(t, path)) != 0 {
		t.Error("the filesystem changed")
	}
}

func TestApplyInUnwritableDirectoryFailsBeforeCaddyRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "Caddyfile")
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	res, err := call(t, p, "apply", withAck(baseArgs(path)))
	if err != nil || !res.IsError || len(run.calls) != 0 {
		t.Fatalf("result = %v %s, calls %v", err, res.Content, run.calls)
	}
}

func TestApplyRefusesPublicBind(t *testing.T) {
	path := tempConfig(t, previousConfig)
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	args := withAck(baseArgs(path))
	args["bind"] = "0.0.0.0"
	res, err := call(t, p, "apply", args)
	if err == nil && !res.IsError {
		t.Fatal("a wildcard bind was applied")
	}
	if len(run.calls) != 0 || readFile(t, path) != previousConfig {
		t.Fatal("a refused apply had an effect")
	}
}

func TestValidateGetsTheTokenOnlyInTheChildEnvironment(t *testing.T) {
	run := &fakeRunner{reply: func(c fakeCall) ([]byte, error) {
		return []byte("echo " + sentinelToken), errors.New("exit status 1")
	}}
	p := newTestPlugin(t, run, map[string]string{SecretCloudflareToken: sentinelToken})
	res, _ := call(t, p, "validate", nil)
	if len(run.calls[0].env) != 1 {
		t.Errorf("env = %v", run.calls[0].env)
	}
	assertNoToken(t, "validate result", string(res.Content))
}
