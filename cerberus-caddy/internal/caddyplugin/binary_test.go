package caddyplugin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func resolverWith(configured string, executable map[string]bool, onPath string) binaryResolver {
	r := newBinaryResolver(configured)
	r.isExec = func(p string) bool { return executable[p] }
	r.lookPath = func(string) (string, error) {
		if onPath == "" {
			return "", errors.New("not found")
		}
		return onPath, nil
	}
	return r
}

func TestResolverSearchesFallbacksInOrderBeforePATH(t *testing.T) {
	got, err := resolverWith("", map[string]bool{"/usr/local/bin/caddy": true, "/snap/bin/caddy": true}, "/path/caddy").resolve()
	if err != nil || got != "/usr/local/bin/caddy" {
		t.Fatalf("resolve = %q, %v; want /usr/local/bin/caddy", got, err)
	}
	got, err = resolverWith("", map[string]bool{"/snap/bin/caddy": true}, "/path/caddy").resolve()
	if err != nil || got != "/snap/bin/caddy" {
		t.Fatalf("resolve = %q, %v; want the snap path", got, err)
	}
}

func TestResolverFallsBackToPATHLast(t *testing.T) {
	got, err := resolverWith("", nil, "/somewhere/caddy").resolve()
	if err != nil || got != "/somewhere/caddy" {
		t.Fatalf("resolve = %q, %v", got, err)
	}
}

func TestResolverConfiguredPathWinsAndIsNeverSilentlyReplaced(t *testing.T) {
	got, err := resolverWith("/opt/custom/caddy", map[string]bool{"/opt/custom/caddy": true, "/usr/bin/caddy": true}, "").resolve()
	if err != nil || got != "/opt/custom/caddy" {
		t.Fatalf("resolve = %q, %v", got, err)
	}
	_, err = resolverWith("/opt/custom/caddy", map[string]bool{"/usr/bin/caddy": true}, "").resolve()
	if !errors.Is(err, errCaddyNotFound) || !strings.Contains(err.Error(), "/opt/custom/caddy") {
		t.Fatalf("a bad configured path must be an error naming it, got %v", err)
	}
}

func TestResolverNotFoundNamesWhereItLooked(t *testing.T) {
	_, err := resolverWith("", nil, "").resolve()
	if !errors.Is(err, errCaddyNotFound) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"/usr/bin/caddy", "/usr/local/bin/caddy", "/opt/homebrew/bin/caddy", "/snap/bin/caddy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestHealthReportsAMissingBinary(t *testing.T) {
	p := New()
	p.resolve = func() (string, error) { return "", errCaddyNotFound }
	status, err := p.Health(context.Background())
	if err != nil || status.OK {
		t.Fatalf("Health = %+v, %v; want not ok", status, err)
	}
	p.resolve = func() (string, error) { return "/usr/bin/caddy", nil }
	if status, _ = p.Health(context.Background()); !status.OK {
		t.Fatalf("Health = %+v; want ok", status)
	}
}

func TestMissingBinaryIsConnectorUnavailable(t *testing.T) {
	p := New()
	p.run = &fakeRunner{}
	p.resolve = func() (string, error) { return "", errCaddyNotFound }
	res, err := call(t, p, "reload", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(string(res.Content), "connector_unavailable") {
		t.Fatalf("result = %+v %s; want a connector_unavailable error", res, res.Content)
	}
}

func TestReloadAndValidateUseTheResolvedBinary(t *testing.T) {
	run := &fakeRunner{}
	p := newTestPlugin(t, run, nil)
	if _, err := call(t, p, "reload", map[string]any{"config_path": "/x/Caddyfile"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(t, p, "validate", nil); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 2 || run.calls[0].bin != "/fake/bin/caddy" || run.calls[0].args[0] != "reload" ||
		run.calls[0].args[2] != "/x/Caddyfile" || run.calls[1].args[0] != "validate" {
		t.Fatalf("calls = %+v", run.calls)
	}
}
