package sysmonplugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// Env is everything the collectors need from the machine. Production uses
// RealEnv; tests substitute recorded files and command output.
type Env struct {
	GOOS     string
	GOARCH   string
	ReadFile func(path string) ([]byte, error)
	// Run executes a read-only tool by bare name, resolving it per call.
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	Hostname func() (string, error)
	Now      func() time.Time
	Sleep    func(time.Duration)
}

// searchDirs are consulted after PATH, because a daemon started by launchd or
// a systemd user manager carries a minimal PATH (AGENTS.md).
var searchDirs = []string{
	"/usr/bin", "/bin", "/usr/sbin", "/sbin",
	"/usr/local/bin", "/opt/homebrew/bin",
	"/opt/rocm/bin", "/usr/lib/rocm/bin",
	"/usr/local/nvidia/bin", "/usr/local/cuda/bin",
}

// resolveTool finds name on PATH, then in searchDirs. Resolved on every call.
func resolveTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range searchDirs {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found", name)
}

// RealEnv reads the live machine. Every command it can run is a read-only
// inspection tool.
func RealEnv() Env {
	return Env{
		GOOS:     runtime.GOOS,
		GOARCH:   runtime.GOARCH,
		ReadFile: os.ReadFile,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			path, err := resolveTool(name)
			if err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, path, args...).Output()
		},
		Hostname: os.Hostname,
		Now:      time.Now,
		Sleep:    time.Sleep,
	}
}
