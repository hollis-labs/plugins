package caddyplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/subprocess"
)

// Argument keys the host adds to a call itself, from --dry-run and --ack.
const (
	argDryRun       = "dry_run"
	argAcknowledged = "acknowledged"
)

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	}
	return false
}

func jsonResult(value any, isErr bool) (subprocess.MCPCallResult, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return subprocess.MCPCallResult{IsError: isErr, Content: content}, nil
}

// current is the file at path as it stands: its bytes, mode and whether it
// exists. A missing file is not an error, it is the first apply.
type current struct {
	text   string
	exists bool
	mode   fs.FileMode
}

func readCurrent(path string) (current, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return current{mode: 0o644}, nil
	}
	if err != nil {
		return current{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed, fmt.Errorf("reading %s: %w", path, err))
	}
	mode := fs.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return current{text: string(data), exists: true, mode: mode}, nil
}

// warnings that apply to a rendered configuration whatever the outcome.
func (p *Plugin) renderWarnings(in RenderInput) []string {
	if in.TLS != TLSCloudflareDNS {
		return nil
	}
	w := []string{"The Caddyfile reads the DNS-01 token from the " + in.TokenEnv + " environment variable of the running caddy process. " +
		"This plugin cannot set that for you: supply it to the caddy service (see the plugin README)."}
	if p.token == "" {
		w = append(w, "No cloudflare_api_token secret reached this plugin, so a real validate runs without the token.")
	}
	return w
}

func (p *Plugin) render(args map[string]any) (subprocess.MCPCallResult, error) {
	in, err := parseRenderInput(args)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	cur, err := readCurrent(in.ConfigPath)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	return jsonResult(p.buildRender(in, cur), false)
}

func (p *Plugin) buildRender(in RenderInput, cur current) RenderResult {
	text := renderCaddyfile(in)
	routes := make([]RenderedRoute, 0, len(in.Routes))
	for _, r := range in.Routes {
		routes = append(routes, RenderedRoute{Name: r.Name, FQDN: r.fqdn(), Upstream: loopbackUpstream + ":" + fmt.Sprint(r.Port)})
	}
	return RenderResult{
		ConfigPath:       in.ConfigPath,
		Bind:             in.Bind.String(),
		TLS:              in.TLS,
		Routes:           routes,
		Caddyfile:        text,
		CurrentExists:    cur.exists,
		Changed:          cur.text != text,
		Diff:             unifiedDiff(diffName(in.ConfigPath, cur.exists), in.ConfigPath+" (rendered)", cur.text, text),
		TokenPlaceholder: in.tokenPlaceholder(),
		Warnings:         p.renderWarnings(in),
	}
}

func diffName(path string, exists bool) string {
	if !exists {
		return "/dev/null"
	}
	return path
}

// apply writes the rendered file, validating it before it replaces anything
// and reloading afterwards. The order is what makes it safe to run:
//
//  1. the candidate is written to a temporary file beside the target and
//     validated there, so an invalid file never becomes the live one;
//  2. it is renamed over the target (atomic on one filesystem);
//  3. caddy is reloaded; if that fails the previous file is put back.
func (p *Plugin) apply(ctx context.Context, args map[string]any) (subprocess.MCPCallResult, error) {
	dryRun := truthy(args[argDryRun])
	if !dryRun && !truthy(args[argAcknowledged]) {
		return subprocess.MCPCallResult{}, invalid("apply replaces the Caddy configuration and requires acknowledgment. Re-run it acknowledged (--ack), or preview it first with --dry-run")
	}
	in, err := parseRenderInput(args)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	cur, err := readCurrent(in.ConfigPath)
	if err != nil {
		return subprocess.MCPCallResult{}, err
	}
	rendered := p.buildRender(in, cur)
	res := ApplyResult{
		DryRun:     dryRun,
		ConfigPath: in.ConfigPath,
		Changed:    rendered.Changed,
		Diff:       rendered.Diff,
		Warnings:   rendered.Warnings,
	}

	if dryRun {
		if rendered.Changed {
			res.Summary = "Would write " + in.ConfigPath + ", validate it, and reload caddy; rolling back if either fails."
		} else {
			res.Summary = in.ConfigPath + " already matches the rendered configuration; nothing would change."
		}
		return jsonResult(res, false)
	}
	if !rendered.Changed {
		res.Summary = in.ConfigPath + " already matches the rendered configuration; nothing was changed."
		return jsonResult(res, false)
	}

	var env []string
	if in.TLS == TLSCloudflareDNS && p.token != "" {
		env = []string{in.TokenEnv + "=" + p.token}
	}

	dir := filepath.Dir(in.ConfigPath)
	tmp, err := os.CreateTemp(dir, ".Caddyfile.cerberus-*.tmp")
	if err != nil {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed,
			fmt.Errorf("cannot stage a candidate beside %s (the directory must be writable by the user running cerberus): %w", in.ConfigPath, err))
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.WriteString(rendered.Caddyfile); err != nil {
		_ = tmp.Close()
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed, fmt.Errorf("writing candidate: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed, fmt.Errorf("writing candidate: %w", err))
	}
	if err := os.Chmod(tmpPath, cur.mode); err != nil {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed, fmt.Errorf("setting candidate mode: %w", err))
	}

	// 1. Validate the candidate. The live file has not been touched.
	out, err := p.caddyCommand(ctx, []string{"validate", "--adapter", "caddyfile", "--config", tmpPath}, env)
	if err != nil {
		if isCoded(err) {
			return subprocess.MCPCallResult{}, err
		}
		res.Summary = "caddy rejected the rendered configuration; " + in.ConfigPath + " was not changed."
		res.CaddyOutput = p.scrub.text(string(out))
		return jsonResult(res, true)
	}
	res.Validated = true

	// 2. Swap the candidate in.
	if err := os.Rename(tmpPath, in.ConfigPath); err != nil {
		return subprocess.MCPCallResult{}, cerbplugin.WithCode(cerbplugin.ErrorOperationFailed, fmt.Errorf("replacing %s: %w", in.ConfigPath, err))
	}

	// 3. Reload; put the previous file back if caddy refuses it.
	out, err = p.caddyCommand(ctx, []string{"reload", "--adapter", "caddyfile", "--config", in.ConfigPath}, nil)
	if err != nil {
		if isCoded(err) {
			res.Summary = "caddy could not be run to reload; restoring the previous configuration."
		} else {
			res.Summary = "caddy reload failed; restoring the previous configuration."
			res.CaddyOutput = p.scrub.text(string(out))
		}
		if rbErr := restore(in.ConfigPath, cur); rbErr != nil {
			res.Summary += " Restoring " + in.ConfigPath + " FAILED: " + p.scrub.text(rbErr.Error()) + ". The file on disk is the new, unloaded configuration."
		} else {
			res.RolledBack = true
			res.Summary += " " + in.ConfigPath + " is back to its previous content."
		}
		return jsonResult(res, true)
	}
	res.Reloaded = true
	res.Applied = true
	res.Summary = "Wrote " + in.ConfigPath + ", validated it, and reloaded caddy."
	return jsonResult(res, false)
}

// restore puts the previous file back, or removes the file if there was none.
func restore(path string, prev current) error {
	if !prev.exists {
		return os.Remove(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".Caddyfile.cerberus-restore-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.WriteString(prev.text); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, prev.mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func isCoded(err error) bool {
	var coded *cerbplugin.CodedError
	return errors.As(err, &coded)
}
