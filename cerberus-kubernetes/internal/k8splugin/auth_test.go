package k8splugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// writeKubeconfig lays down a kubeconfig covering the auth modes that matter:
// an exec credential plugin that is not installed and wants a terminal, and a
// static token. Both users carry the sentinel so the DTO tests can assert it
// never escapes.
func writeKubeconfig(t *testing.T, dir string) string {
	t.Helper()
	body := `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod-cluster
  cluster:
    server: https://api.prod.example.com:6443
contexts:
- name: prod
  context:
    cluster: prod-cluster
    user: prod-user
    namespace: apps
- name: dev
  context:
    cluster: prod-cluster
    user: token-user
users:
- name: prod-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: cerberus-test-missing-helper
      args: ["get-token", "--client-secret=` + sentinel + `"]
      env:
      - name: AAD_CLIENT_SECRET
        value: ` + sentinel + `
      installHint: install the helper from the internal package repo
      interactiveMode: Always
- name: token-user
  user:
    token: ` + sentinel + `
`
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func TestClassifyAuthModeCoversEveryKubeconfigShape(t *testing.T) {
	cases := []struct {
		name string
		info *clientcmdapi.AuthInfo
		want AuthMode
	}{
		{"nil", nil, AuthModeAnonymous},
		{"empty", &clientcmdapi.AuthInfo{}, AuthModeAnonymous},
		{"exec", &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{Command: "helper"}}, AuthModeExec},
		{"token", &clientcmdapi.AuthInfo{Token: "t"}, AuthModeToken},
		{"token file", &clientcmdapi.AuthInfo{TokenFile: "/tmp/t"}, AuthModeTokenFile},
		{"client cert", &clientcmdapi.AuthInfo{ClientCertificate: "/tmp/c.pem"}, AuthModeClientCert},
		{"basic", &clientcmdapi.AuthInfo{Username: "admin"}, AuthModeBasic},
		{"auth provider", &clientcmdapi.AuthInfo{AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "oidc"}}, AuthModeAuthProvider},
		// exec wins over a stale token left beside it, matching client-go's
		// own precedence — otherwise we would report a mode the request does
		// not actually use.
		{"exec beats token", &clientcmdapi.AuthInfo{Token: "t", Exec: &clientcmdapi.ExecConfig{Command: "helper"}}, AuthModeExec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAuthMode(tc.info); got != tc.want {
				t.Errorf("ClassifyAuthMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// list_contexts is the one operation that works with no cluster, no credential
// and no network, which makes it the first useful thing this connector can do.
func TestContextsReadsTheKubeconfigWithoutAnyClusterContact(t *testing.T) {
	path := writeKubeconfig(t, t.TempDir())

	contexts, err := Contexts(ClusterOptions{Kubeconfig: path})
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(contexts) != 2 {
		t.Fatalf("got %d contexts, want 2", len(contexts))
	}
	// Sorted by name, so dev comes first.
	if contexts[0].Name != "dev" || contexts[0].AuthMode != string(AuthModeToken) {
		t.Errorf("dev context = %+v", contexts[0])
	}
	prod := contexts[1]
	if prod.Name != "prod" || !prod.IsCurrent {
		t.Errorf("prod context = %+v, want the current one", prod)
	}
	if prod.AuthMode != string(AuthModeExec) {
		t.Errorf("prod auth mode = %q, want exec", prod.AuthMode)
	}
	if prod.Server != "https://api.prod.example.com:6443" || prod.Namespace != "apps" {
		t.Errorf("prod context lost its cluster or namespace: %+v", prod)
	}
}

// The two failures most likely on a corporate cluster are both local and both
// invisible in a 401: the credential helper is missing from the daemon's
// minimal PATH, and it wants a terminal that a Cerberus-launched process does
// not have. Preflight must name both without contacting anything.
func TestPreflightNamesAMissingCredentialHelperAndTheInteractiveRequirement(t *testing.T) {
	path := writeKubeconfig(t, t.TempDir())

	check, err := Preflight(ClusterOptions{Kubeconfig: path, Context: "prod"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if check.Ready {
		t.Error("Ready = true, want false when the credential helper is not installed")
	}
	if check.AuthMode != string(AuthModeExec) {
		t.Fatalf("auth mode = %q, want exec", check.AuthMode)
	}
	if check.CredentialPlugin == nil || check.CredentialPlugin.Resolved {
		t.Fatalf("credential plugin = %+v, want an unresolved one", check.CredentialPlugin)
	}
	if check.CredentialPlugin.InstallHint == "" {
		t.Error("install hint dropped; the cluster owner's instructions are better than ours")
	}

	problems := strings.Join(check.Problems, "\n")
	if !strings.Contains(problems, "cerberus-test-missing-helper") {
		t.Errorf("problems do not name the missing binary: %q", problems)
	}
	if !strings.Contains(problems, "without a terminal") || !strings.Contains(problems, "IfAvailable") {
		t.Errorf("problems do not name the TTY requirement and its fix: %q", problems)
	}
}

// Against a real cluster, a resolvable helper set to interactiveMode Always
// passed preflight as ready and then failed every call: client-go refuses to
// run it without a terminal. Ready has to say so up front.
func TestPreflightIsNotReadyForAnAlwaysInteractiveHelperEvenWhenItResolves(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "cerberus-test-present-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config")
	body := `apiVersion: v1
kind: Config
current-context: sso
clusters:
- name: c
  cluster: {server: https://api.example.com:6443}
contexts:
- name: sso
  context: {cluster: c, user: sso}
users:
- name: sso
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: cerberus-test-present-helper
      interactiveMode: Always
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	check, err := Preflight(ClusterOptions{Kubeconfig: path, CredentialPath: dir})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if check.CredentialPlugin == nil || !check.CredentialPlugin.Resolved {
		t.Fatalf("helper did not resolve: %+v", check.CredentialPlugin)
	}
	if check.Ready {
		t.Error("Ready = true for a helper client-go will refuse to run")
	}
}

func TestPreflightResolvesAHelperOutsideThePATH(t *testing.T) {
	// The whole point: the daemon's PATH is /usr/bin:/bin:/usr/sbin:/sbin, so a
	// helper installed anywhere normal is invisible to exec.LookPath. It must
	// still be found, and the absolute path reported.
	binDir := t.TempDir()
	helper := filepath.Join(binDir, "cerberus-test-present-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: c
  cluster:
    server: https://api.example.com
contexts:
- name: prod
  context: {cluster: c, user: u}
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: cerberus-test-present-helper
      interactiveMode: Never
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	check, err := Preflight(ClusterOptions{Kubeconfig: path, CredentialPath: binDir})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !check.Ready {
		t.Fatalf("Ready = false, problems: %v", check.Problems)
	}
	if check.CredentialPlugin.ResolvedPath != helper {
		t.Errorf("resolved path = %q, want %q", check.CredentialPlugin.ResolvedPath, helper)
	}
}

func TestPreflightPrefersAnExplicitHostSecretOverTheKubeconfig(t *testing.T) {
	path := writeKubeconfig(t, t.TempDir())

	check, err := Preflight(ClusterOptions{
		Kubeconfig: path,
		Server:     "https://api.example.com",
		Token:      "a-token",
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if check.AuthMode != string(AuthModeHostSecret) || !check.Ready {
		t.Fatalf("check = %+v, want a ready host-secret mode", check)
	}
}

func TestPreflightReportsAnUnknownContextByName(t *testing.T) {
	path := writeKubeconfig(t, t.TempDir())

	check, err := Preflight(ClusterOptions{Kubeconfig: path, Context: "nope"})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(check.Problems) == 0 || !strings.Contains(check.Problems[0], "nope") {
		t.Errorf("problems = %v, want the unknown context named", check.Problems)
	}
}

// A token with no server is a half-configured connector, and the recovery is
// one field away. Saying so beats a dial to an empty host.
func TestRestConfigRefusesATokenWithNoServer(t *testing.T) {
	_, _, err := RestConfig(ClusterOptions{Token: "a-token"})
	if err == nil {
		t.Fatal("want an error naming the missing server field")
	}
	if !strings.Contains(err.Error(), ConfigServer) {
		t.Errorf("error does not name the field to set: %v", err)
	}
}

func TestResolveCredentialCommandReportsWhereItLooked(t *testing.T) {
	_, err := ResolveCredentialCommand("cerberus-test-missing-helper", "")
	if err == nil {
		t.Fatal("want an error for a helper that is not installed")
	}
	msg := err.Error()
	if code := codeOf(err); code != cerbplugin.ErrorCredentialMissing {
		t.Errorf("code = %q, want credential_missing: %q", code, msg)
	}
	if !strings.Contains(msg, CredentialPathEnvVar) {
		t.Errorf("error does not name the escape hatch: %q", msg)
	}
}
