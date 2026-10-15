package caddyplugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ConfigCaddyPath is the optional plugin config key naming the caddy binary.
const ConfigCaddyPath = "caddy_path"

// fallbackBinaryPaths are searched when nothing is configured. A daemon
// started by launchd or a systemd user manager has a minimal PATH, so a tool
// that resolves in a login shell may not resolve here; these are the places a
// package manager puts it.
var fallbackBinaryPaths = []string{
	"/usr/bin/caddy",
	"/usr/local/bin/caddy",
	"/opt/homebrew/bin/caddy",
	"/snap/bin/caddy",
	"/var/lib/snapd/snap/bin/caddy",
}

// errCaddyNotFound is returned when no caddy binary could be located.
var errCaddyNotFound = errors.New("caddy binary not found")

// binaryResolver locates caddy on every call; the result is never cached, so a
// caddy installed after the plugin loaded is found without a reload.
type binaryResolver struct {
	configured string
	fallbacks  []string
	lookPath   func(string) (string, error)
	isExec     func(string) bool
}

func newBinaryResolver(configured string) binaryResolver {
	return binaryResolver{
		configured: strings.TrimSpace(configured),
		fallbacks:  fallbackBinaryPaths,
		lookPath:   exec.LookPath,
		isExec:     isExecutableFile,
	}
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// resolve tries the configured path, then the explicit fallbacks, then PATH as
// a last resort. A configured path that is not executable is an error rather
// than a silent fall-through: the operator asked for that binary.
func (r binaryResolver) resolve() (string, error) {
	if r.configured != "" {
		if r.isExec(r.configured) {
			return r.configured, nil
		}
		return "", fmt.Errorf("%w: configured %s %q is not an executable file", errCaddyNotFound, ConfigCaddyPath, r.configured)
	}
	for _, path := range r.fallbacks {
		if r.isExec(path) {
			return path, nil
		}
	}
	if path, err := r.lookPath("caddy"); err == nil && path != "" {
		return path, nil
	}
	return "", fmt.Errorf("%w: looked in %s and PATH", errCaddyNotFound, strings.Join(r.fallbacks, ", "))
}

// runner executes a command. It is the seam tests replace with a fake.
type runner interface {
	// Run executes bin with args and extraEnv added to the plugin's own
	// environment, and returns combined stdout and stderr.
	Run(ctx context.Context, bin string, args []string, extraEnv []string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, bin string, args []string, extraEnv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}
