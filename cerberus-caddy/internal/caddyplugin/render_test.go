package caddyplugin

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden Caddyfiles")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.Caddyfile")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from golden.\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func mustParse(t *testing.T, raw map[string]any) RenderInput {
	t.Helper()
	in, err := parseRenderInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestRenderGoldenCloudflareDNS(t *testing.T) {
	in := mustParse(t, map[string]any{
		"bind":  "100.64.0.10",
		"email": "ops@example.com",
		"routes": []any{
			map[string]any{"name": "web", "host": "hub", "domain": "example.com", "port": float64(8990)},
			map[string]any{"name": "api", "host": "hub", "domain": "example.com", "port": "8090"},
			map[string]any{"name": "docs", "domain": "example.test", "port": float64(4000)},
			map[string]any{"name": "app", "host": "old", "domain": "example.com", "port": float64(8080)},
		},
	})
	golden(t, "cloudflare_dns", renderCaddyfile(in))
}

func TestRenderGoldenInternalTLS(t *testing.T) {
	in := mustParse(t, map[string]any{
		"bind": "fd7a:115c:a1e0::1",
		"tls":  "internal",
		"routes": []any{
			map[string]any{"name": "app", "host": "hub", "domain": "example.test", "port": float64(8080)},
		},
	})
	golden(t, "internal", renderCaddyfile(in))
}

func TestRenderIsDeterministicWhateverTheRouteOrder(t *testing.T) {
	a := mustParse(t, baseArgs("/x"))
	b := a
	b.Routes = []Route{a.Routes[1], a.Routes[0]}
	if renderCaddyfile(a) != renderCaddyfile(b) {
		t.Fatal("route order changed the rendered file")
	}
}

func TestRenderBindsOnlyToTheSuppliedAddress(t *testing.T) {
	text := renderCaddyfile(mustParse(t, baseArgs("/x")))
	if strings.Count(text, "default_bind 100.64.0.10") != 1 {
		t.Fatalf("expected exactly one default_bind:\n%s", text)
	}
	for _, bad := range []string{"0.0.0.0", "[::]"} {
		if strings.Contains(text, bad) {
			t.Errorf("rendered file contains %q:\n%s", bad, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "bind "):
			t.Errorf("per-site bind directive would override default_bind: %q", line)
		case strings.HasPrefix(trimmed, "reverse_proxy"):
			if !strings.HasPrefix(trimmed, "reverse_proxy 127.0.0.1:") {
				t.Errorf("upstream is not loopback: %q", line)
			}
		case strings.HasPrefix(trimmed, "*.") && strings.HasSuffix(trimmed, "{"):
			// A site address with no port, so no extra listener is declared.
			if strings.Contains(trimmed, ":") {
				t.Errorf("site address carries a port: %q", line)
			}
		}
	}
}

func TestRenderReferencesTheTokenByPlaceholderOnly(t *testing.T) {
	raw := baseArgs("/x")
	raw["token_env"] = "EXAMPLE_DNS_TOKEN"
	text := renderCaddyfile(mustParse(t, raw))
	if !strings.Contains(text, "dns cloudflare {env.EXAMPLE_DNS_TOKEN}") {
		t.Fatalf("missing placeholder:\n%s", text)
	}
}

func TestParseRejectsUnsafeInput(t *testing.T) {
	cases := map[string]func(map[string]any){
		"wildcard bind":    func(a map[string]any) { a["bind"] = "0.0.0.0" },
		"v6 wildcard bind": func(a map[string]any) { a["bind"] = "::" },
		"public bind":      func(a map[string]any) { a["bind"] = "203.0.113.7" },
		"lan bind":         func(a map[string]any) { a["bind"] = "192.168.1.5" },
		"hostname bind":    func(a map[string]any) { a["bind"] = "example.test" },
		"missing bind":     func(a map[string]any) { delete(a, "bind") },
		"no routes":        func(a map[string]any) { a["routes"] = []any{} },
		"unknown tls mode": func(a map[string]any) { a["tls"] = "http-01" },
		"bad token env":    func(a map[string]any) { a["token_env"] = "x}\n{" },
		"bad email":        func(a map[string]any) { a["email"] = "a b@example.com" },
		"unknown route key": func(a map[string]any) {
			a["routes"] = []any{map[string]any{"name": "a", "domain": "example.com", "port": 1, "upstream": "10.0.0.1:80"}}
		},
		"name injection": func(a map[string]any) {
			a["routes"] = []any{map[string]any{"name": "a }\n evil {", "domain": "example.com", "port": float64(80)}}
		},
		"domain injection": func(a map[string]any) {
			a["routes"] = []any{map[string]any{"name": "a", "domain": "example.com {\nabort", "port": float64(80)}}
		},
		"port zero": func(a map[string]any) {
			a["routes"] = []any{map[string]any{"name": "a", "domain": "example.com", "port": float64(0)}}
		},
		"port range": func(a map[string]any) {
			a["routes"] = []any{map[string]any{"name": "a", "domain": "example.com", "port": float64(70000)}}
		},
		"duplicate": func(a map[string]any) {
			r := map[string]any{"name": "a", "domain": "example.com", "port": float64(80)}
			a["routes"] = []any{r, r}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			raw := baseArgs("/x")
			mutate(raw)
			if _, err := parseRenderInput(raw); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

func TestParseAcceptsRoutesAsCLIJSON(t *testing.T) {
	in := mustParse(t, map[string]any{
		"bind":   "100.64.0.10",
		"routes": `[{"name":"app","host":"hub","domain":"example.com","port":8080}]`,
	})
	if len(in.Routes) != 1 || in.Routes[0].fqdn() != "app.hub.example.com" {
		t.Fatalf("routes = %+v", in.Routes)
	}
}

func TestRenderOperationDiffsAgainstTheCurrentFile(t *testing.T) {
	path := tempConfig(t, "old line\n")
	p := newTestPlugin(t, &fakeRunner{}, nil)
	res, err := call(t, p, "render", baseArgs(path))
	if err != nil || res.IsError {
		t.Fatalf("render: %v %s", err, res.Content)
	}
	out := decode[RenderResult](t, res)
	if !out.Changed || !out.CurrentExists {
		t.Fatalf("result = %+v", out)
	}
	if !strings.Contains(out.Diff, "-old line") || !strings.Contains(out.Diff, "+\tdefault_bind 100.64.0.10") {
		t.Errorf("diff does not show the change:\n%s", out.Diff)
	}
	if len(out.Routes) != 2 || out.Routes[0].Upstream == "" {
		t.Errorf("routes = %+v", out.Routes)
	}
	if readFile(t, path) != "old line\n" {
		t.Error("render changed the file")
	}
	// A second render against the rendered text is a no-op.
	if err := os.WriteFile(path, []byte(out.Caddyfile), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = call(t, p, "render", baseArgs(path))
	if again := decode[RenderResult](t, res); again.Changed || again.Diff != "" {
		t.Errorf("rendered file should be unchanged: %+v", again)
	}
}

func TestRenderWithoutACurrentFileDiffsFromNothing(t *testing.T) {
	path := tempConfig(t, "")
	p := newTestPlugin(t, &fakeRunner{}, nil)
	res, _ := call(t, p, "render", baseArgs(path))
	out := decode[RenderResult](t, res)
	if out.CurrentExists || !strings.HasPrefix(out.Diff, "--- /dev/null\n") {
		t.Fatalf("result = %+v", out)
	}
}

func TestRenderNeverEmitsTheToken(t *testing.T) {
	path := tempConfig(t, "")
	p := newTestPlugin(t, &fakeRunner{}, map[string]string{SecretCloudflareToken: sentinelToken})
	res, _ := call(t, p, "render", baseArgs(path))
	assertNoToken(t, "render result", string(res.Content))
	if !strings.Contains(string(res.Content), "{env.CLOUDFLARE_API_TOKEN}") {
		t.Error("expected the placeholder in the result")
	}
}

func TestRenderRefusesPublicBindThroughTheOperation(t *testing.T) {
	p := newTestPlugin(t, &fakeRunner{}, nil)
	args := baseArgs(tempConfig(t, ""))
	args["bind"] = "0.0.0.0"
	res, err := call(t, p, "render", args)
	if err == nil && !res.IsError {
		t.Fatalf("a wildcard bind was accepted: %s", res.Content)
	}
}
