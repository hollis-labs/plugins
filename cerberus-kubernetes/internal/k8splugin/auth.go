package k8splugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// How this cluster authenticates is not known yet — the cluster does not exist
// at the time of writing (WP-K0 in docs/plans/k8s-connector-plugin.md is an
// open question to whoever provisions it). So rather than guess one mechanism
// and rebuild later, every plausible answer is a named mode here, the plugin
// classifies whichever one it is handed, and the operator can see the
// classification with `check_access` before anything is attempted.
type AuthMode string

const (
	// AuthModeHostSecret is a static bearer token Cerberus resolved from its
	// own secret provider and handed over in init config, paired with an
	// explicit server URL. No kubeconfig involved. This is the mode to prefer
	// if the cluster will give us a ServiceAccount: it is non-interactive,
	// attributable, and immune to every PATH and TTY problem below.
	AuthModeHostSecret AuthMode = "host-secret"

	// AuthModeInCluster is the ServiceAccount mounted into a pod. Only reachable
	// if Cerberus itself ever runs inside the cluster; detected, not assumed.
	AuthModeInCluster AuthMode = "in-cluster"

	// The remaining modes are classifications of a kubeconfig auth-info.
	AuthModeExec         AuthMode = "exec"               // external credential plugin
	AuthModeToken        AuthMode = "token"              // static token in the kubeconfig
	AuthModeTokenFile    AuthMode = "token-file"         // token read from a file
	AuthModeClientCert   AuthMode = "client-certificate" // mTLS
	AuthModeBasic        AuthMode = "basic"              // username/password
	AuthModeAuthProvider AuthMode = "auth-provider"      // legacy oidc/gcp/azure providers
	AuthModeAnonymous    AuthMode = "anonymous"          // no credential at all
	AuthModeUnknown      AuthMode = "unknown"
)

// ClusterOptions is everything that selects and authenticates a connection,
// resolved per call. Boot-time resolution is what produced the Docker
// connector's silent outage — see AGENTS.md, "The daemon's environment is not
// your shell's" — so nothing here is cached across operations.
type ClusterOptions struct {
	// Kubeconfig is an explicit path. Empty falls back to $KUBECONFIG and then
	// ~/.kube/config, via client-go's own loading rules.
	Kubeconfig string

	// Context selects a kubeconfig context. Empty uses the current-context.
	// Per call, not per process: one daemon, several clusters, and the next
	// call must not inherit this one's choice.
	Context string

	// Server and Token together select AuthModeHostSecret. Token is resolved by
	// the host from its secret provider; this plugin never reads a credential
	// store itself.
	Server string
	Token  string

	// CredentialPath is an extra PATH-style list searched for an exec
	// credential plugin, ahead of the built-in fallbacks.
	CredentialPath string
}

// credentialSearchDirs are the fallback locations searched for an exec
// credential helper, in order, after CredentialPath and the inherited PATH.
//
// This list exists because of a documented outage. launchd hands the Cerberus
// daemon PATH=/usr/bin:/bin:/usr/sbin:/sbin, which contains none of kubelogin,
// az, aws or gke-gcloud-auth-plugin. The Docker connector resolved its binary
// once at boot, failed, and cached that failure for the daemon's lifetime while
// `cerberus connectors list` reported it healthy. Resolution here is explicit,
// per call, and reports what it searched when it comes up empty.
var credentialSearchDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/opt/local/bin",
	"/usr/bin",
	"/bin",
	"~/.azure/bin",
	"~/bin",
	"~/.local/bin",
	"~/google-cloud-sdk/bin",
	"/opt/homebrew/share/google-cloud-sdk/bin",
	"/usr/local/share/google-cloud-sdk/bin",
}

// CredentialPathEnvVar lets an operator add search locations without a rebuild,
// which is the cheap escape hatch when a cluster turns up using a helper nobody
// anticipated.
const CredentialPathEnvVar = "CERBERUS_KUBE_CREDENTIAL_PATH"

// LoadKubeconfig reads and merges kubeconfig files by client-go's own rules.
// It makes no network call, so it works before anything else does.
func LoadKubeconfig(path string) (*clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules = &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	}
	cfg, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	return cfg, nil
}

// ClassifyAuthMode reports how an auth-info authenticates. The order matters:
// a kubeconfig may carry several fields and client-go's own precedence puts
// exec first.
func ClassifyAuthMode(info *clientcmdapi.AuthInfo) AuthMode {
	switch {
	case info == nil:
		return AuthModeAnonymous
	case info.Exec != nil:
		return AuthModeExec
	case info.AuthProvider != nil:
		return AuthModeAuthProvider
	case info.Token != "":
		return AuthModeToken
	case info.TokenFile != "":
		return AuthModeTokenFile
	case info.ClientCertificate != "" || len(info.ClientCertificateData) > 0:
		return AuthModeClientCert
	case info.Username != "":
		return AuthModeBasic
	default:
		return AuthModeAnonymous
	}
}

// Contexts lists every kubeconfig context with its classified auth mode. This
// is the one operation that needs no cluster, no credential and no network,
// which makes it the first useful thing this connector can do and the fastest
// way to answer WP-K0 for a kubeconfig somebody hands us.
func Contexts(opts ClusterOptions) ([]Context, error) {
	cfg, err := LoadKubeconfig(opts.Kubeconfig)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Context, 0, len(names))
	for _, name := range names {
		kctx := cfg.Contexts[name]
		entry := Context{
			Name:      name,
			Cluster:   kctx.Cluster,
			Namespace: kctx.Namespace,
			User:      kctx.AuthInfo,
			IsCurrent: name == cfg.CurrentContext,
			AuthMode:  string(ClassifyAuthMode(cfg.AuthInfos[kctx.AuthInfo])),
		}
		if cluster := cfg.Clusters[kctx.Cluster]; cluster != nil {
			entry.Server = cluster.Server
		}
		out = append(out, entry)
	}
	return out, nil
}

// Preflight answers "what would happen if we tried", without trying. It is the
// operation to run first when something is wrong, because the two failures most
// likely here — a credential helper missing from the daemon's PATH, and a
// helper that wants a terminal there isn't one of — are both local, both
// invisible in a 401, and both detectable without contacting the cluster.
func Preflight(opts ClusterOptions) (AccessCheck, error) {
	if opts.Token != "" && opts.Server != "" {
		return AccessCheck{
			Context:  "(host-secret)",
			Server:   opts.Server,
			AuthMode: string(AuthModeHostSecret),
			Ready:    true,
		}, nil
	}
	if inClusterAvailable() {
		return AccessCheck{
			Context:  "(in-cluster)",
			AuthMode: string(AuthModeInCluster),
			Ready:    true,
		}, nil
	}

	cfg, err := LoadKubeconfig(opts.Kubeconfig)
	if err != nil {
		return AccessCheck{}, err
	}
	name := opts.Context
	if name == "" {
		name = cfg.CurrentContext
	}
	if name == "" {
		return AccessCheck{
			AuthMode: string(AuthModeUnknown),
			Problems: []string{"kubeconfig sets no current-context and no context was requested; pass a context name, or list them with list_contexts"},
		}, nil
	}
	kctx := cfg.Contexts[name]
	if kctx == nil {
		return AccessCheck{
			Context:  name,
			AuthMode: string(AuthModeUnknown),
			Problems: []string{fmt.Sprintf("no context named %q in the kubeconfig; list them with list_contexts", name)},
		}, nil
	}

	check := AccessCheck{Context: name, Ready: true}
	if cluster := cfg.Clusters[kctx.Cluster]; cluster != nil {
		check.Server = cluster.Server
	}
	info := cfg.AuthInfos[kctx.AuthInfo]
	mode := ClassifyAuthMode(info)
	check.AuthMode = string(mode)

	switch mode {
	case AuthModeExec:
		plugin, problems := inspectCredentialPlugin(info.Exec, opts.CredentialPath)
		check.CredentialPlugin = &plugin
		check.Problems = append(check.Problems, problems...)
		if !plugin.Resolved {
			check.Ready = false
		}
	case AuthModeAnonymous:
		check.Ready = false
		check.Problems = append(check.Problems, fmt.Sprintf(
			"context %q carries no credential; supply one in the kubeconfig, or configure this connector with a server URL and a token secret", name))
	case AuthModeTokenFile:
		if info.TokenFile != "" {
			if _, err := os.Stat(info.TokenFile); err != nil {
				check.Ready = false
				check.Problems = append(check.Problems, fmt.Sprintf(
					"token file %s is not readable from the daemon; check the path and its permissions", info.TokenFile))
			}
		}
	case AuthModeAuthProvider:
		check.Problems = append(check.Problems, fmt.Sprintf(
			"context %q uses the legacy auth-provider mechanism, which client-go has removed for most providers; migrating the kubeconfig entry to an exec credential plugin is the supported path", name))
	}
	return check, nil
}

// inspectCredentialPlugin reports what can be learned about an exec helper
// without running it. It deliberately returns names only: an ExecConfig's Args
// and Env routinely carry a client secret, so neither is emitted.
func inspectCredentialPlugin(execCfg *clientcmdapi.ExecConfig, extraPath string) (CredentialPlugin, []string) {
	plugin := CredentialPlugin{
		Command:         execCfg.Command,
		InstallHint:     execCfg.InstallHint,
		InteractiveMode: string(execCfg.InteractiveMode),
	}
	for _, env := range execCfg.Env {
		plugin.EnvNames = append(plugin.EnvNames, env.Name)
	}

	var problems []string
	path, err := ResolveCredentialCommand(execCfg.Command, extraPath)
	if err == nil {
		plugin.Resolved = true
		plugin.ResolvedPath = path
	} else {
		problems = append(problems, err.Error())
	}

	// A helper that always wants a terminal cannot work under the daemon. This
	// is the same constraint that keeps the SSH tunnel resources on manual
	// start: a Cerberus-launched process has no TTY, and retrying an
	// interactive corporate login on a timer is how an account gets locked out.
	if execCfg.InteractiveMode == clientcmdapi.AlwaysExecInteractiveMode {
		problems = append(problems, fmt.Sprintf(
			"credential plugin %s requires an interactive terminal, which a Cerberus-launched process does not have; refresh the credential in a terminal first, or move this connector to a non-interactive identity", execCfg.Command))
	}
	return plugin, problems
}

// ResolveCredentialCommand finds an exec credential helper, per call, searching
// wider than the inherited PATH. On failure it names the binary and where it
// looked, because "Unauthorized" does not tell an operator that a program is
// missing.
func ResolveCredentialCommand(command, extraPath string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("credential_missing: the kubeconfig declares an exec credential plugin with no command")
	}
	if strings.ContainsRune(command, os.PathSeparator) {
		if info, err := os.Stat(command); err == nil && !info.IsDir() {
			return command, nil
		}
		return "", fmt.Errorf("credential_missing: credential plugin %s is not present at that path", command)
	}
	if path, err := exec.LookPath(command); err == nil {
		return path, nil
	}

	searched := make([]string, 0, len(credentialSearchDirs)+4)
	for _, dir := range splitPathList(extraPath) {
		searched = append(searched, dir)
	}
	for _, dir := range splitPathList(os.Getenv(CredentialPathEnvVar)) {
		searched = append(searched, dir)
	}
	searched = append(searched, credentialSearchDirs...)

	for _, dir := range searched {
		candidate := filepath.Join(expandHome(dir), command)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"credential_missing: credential plugin %s was not found on PATH or in %d fallback locations; the daemon runs under a minimal PATH, so install it somewhere standard or add its directory to %s",
		command, len(searched), CredentialPathEnvVar)
}

// RestConfig builds a connection, choosing between the named modes in a fixed
// order: an explicitly configured host secret wins, then in-cluster, then the
// kubeconfig. It returns the mode it chose so callers can report it.
func RestConfig(opts ClusterOptions) (*rest.Config, AuthMode, error) {
	if opts.Token != "" && opts.Server != "" {
		return &rest.Config{Host: opts.Server, BearerToken: opts.Token}, AuthModeHostSecret, nil
	}
	if opts.Token != "" && opts.Server == "" {
		return nil, AuthModeUnknown, fmt.Errorf(
			"credential_missing: a token is configured but no server URL is; set the %q config field to the API server address", ConfigServer)
	}
	if inClusterAvailable() {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, AuthModeInCluster, fmt.Errorf("in-cluster config: %w", err)
		}
		return cfg, AuthModeInCluster, nil
	}

	raw, err := LoadKubeconfig(opts.Kubeconfig)
	if err != nil {
		return nil, AuthModeUnknown, err
	}
	name := opts.Context
	if name == "" {
		name = raw.CurrentContext
	}
	kctx := raw.Contexts[name]
	if kctx == nil {
		return nil, AuthModeUnknown, fmt.Errorf("no context named %q in the kubeconfig; list them with list_contexts", name)
	}
	info := raw.AuthInfos[kctx.AuthInfo]
	mode := ClassifyAuthMode(info)

	// Rewrite an exec helper to the absolute path we resolved ourselves.
	// client-go would otherwise exec it against the inherited PATH, which under
	// launchd is /usr/bin:/bin:/usr/sbin:/sbin and will not find it. Rewriting a
	// copy keeps the operator's kubeconfig on disk untouched.
	if mode == AuthModeExec {
		resolved, err := ResolveCredentialCommand(info.Exec.Command, opts.CredentialPath)
		if err != nil {
			return nil, mode, err
		}
		raw = raw.DeepCopy()
		raw.AuthInfos[kctx.AuthInfo].Exec.Command = resolved
	}

	clientCfg := clientcmd.NewNonInteractiveClientConfig(*raw, name, &clientcmd.ConfigOverrides{}, nil)
	cfg, err := clientCfg.ClientConfig()
	if err != nil {
		return nil, mode, fmt.Errorf("build client config for context %q: %w", name, err)
	}
	return cfg, mode, nil
}

// inClusterAvailable reports whether this process is running inside a pod with
// a mounted ServiceAccount. Detected rather than assumed, so a stray
// environment variable on a laptop cannot silently change which cluster is
// contacted.
func inClusterAvailable() bool {
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" || os.Getenv("KUBERNETES_SERVICE_PORT") == "" {
		return false
	}
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

func splitPathList(list string) []string {
	if list == "" {
		return nil
	}
	parts := strings.Split(list, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func expandHome(dir string) string {
	if !strings.HasPrefix(dir, "~") {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return dir
	}
	return filepath.Join(home, strings.TrimPrefix(dir, "~"))
}
