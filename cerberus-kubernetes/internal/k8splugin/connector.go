package k8splugin

import (
	"fmt"

	contract "github.com/hollis-labs/cerberus/pkg/connector"
	"github.com/hollis-labs/cerberus/pkg/resource"
)

// ConnectorID is the plugin id, the manifest id and the prefix of every tool
// name the host routes to us. `kubernetes` is not one of the reserved built-in
// ids (ssh, docker, local, github), which a plugin is refused at install for
// claiming.
const ConnectorID = "kubernetes"

// Version is the plugin version, stamped into plugin.yaml.
const Version = "0.1.0"

// Config field and secret names. The manifest declares these and the host hands
// resolved values back under the same keys, so both halves read from one
// constant.
const (
	ConfigKubeconfig     = "kubeconfig"
	ConfigContext        = "context"
	ConfigServer         = "server"
	ConfigNamespace      = "namespace"
	ConfigCredentialPath = "credential_path"

	// SecretToken is optional by design. A kubeconfig-authenticated cluster
	// needs no secret from Cerberus at all, and a missing secret must not fail
	// the load: list_contexts and check_access are the operations an operator
	// needs *because* authentication is not working yet.
	SecretToken = "token"

	// TokenEnvVar is the direct-run fallback for the binary invoked outside the
	// host, which receives no init config. It does not reach the plugin under
	// the daemon: the host's launch environment is an allow-list carrying no
	// credentials by design.
	TokenEnvVar = "CERBERUS_KUBE_TOKEN"
)

// Definition declares what this connector does. The host derives the CLI
// commands, API operations and MCP tool names from it, so adding an operation
// here is the only registration step a plugin needs.
//
// Every operation is read-only. "Work infrastructure is read-only" is a scope
// decision in AGENTS.md, not a permissions workaround: lifecycle and write
// verbs are documented as locked in docs/plans/k8s-connector-plugin.md (WP-K6)
// with what would unlock them, rather than built speculatively. Consequently
// nothing here sets Destructive or SupportsDry — making a read prompt for --ack
// would empty that gate of meaning.
func Definition() contract.Definition {
	return contract.Definition{
		ID:            ConnectorID,
		Version:       Version,
		ResourceTypes: []string{string(resource.Container), string(resource.Server)},
		Capabilities: contract.Capabilities{
			CanHealth: true,
			CanLogs:   true,
		},
		Config: contract.ConfigSchema{
			Fields: []contract.ConfigField{
				{
					Name:        ConfigKubeconfig,
					Type:        "string",
					Description: "Path to a kubeconfig file. Empty uses $KUBECONFIG, then ~/.kube/config.",
				},
				{
					Name:        ConfigContext,
					Type:        "string",
					Description: "Default kubeconfig context. Every operation can override it per call.",
				},
				{
					Name:        ConfigServer,
					Type:        "string",
					Description: "API server URL. Set this with the token secret to authenticate without a kubeconfig.",
				},
				{
					Name:        ConfigNamespace,
					Type:        "string",
					Description: "Default namespace for namespaced reads.",
					Default:     "default",
				},
				{
					Name:        ConfigCredentialPath,
					Type:        "string",
					Description: "Extra PATH-style directories searched for an exec credential plugin. The daemon runs under a minimal PATH that holds none of kubelogin, az or gcloud.",
				},
			},
			Secrets: []contract.SecretRequirement{{
				Name:        SecretToken,
				Description: "Bearer token for the API server, used with the server field. Optional: a kubeconfig-authenticated cluster needs none.",
				Env:         TokenEnvVar,
				Required:    false,
			}},
		},
		Operations: []contract.Operation{
			{
				Name:        "list_contexts",
				Description: "List kubeconfig contexts with the authentication mode each one uses. Reads no cluster and needs no credential, so it works before anything else does.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"kubeconfig": stringProp("Kubeconfig path override."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_contexts"},
			},
			{
				Name:        "check_access",
				Description: "Report what would happen if a request were attempted: the auth mode, whether an exec credential plugin resolves from the daemon's PATH, and whether it needs a terminal there is none of. Run this first when something is wrong.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"context":    stringProp("Kubeconfig context to check."),
					"kubeconfig": stringProp("Kubeconfig path override."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes check_access"},
			},
			{
				Name:        "get_health",
				Description: "Report API server reachability and version. Names what actually failed — a refused dial on a VPN-only endpoint is the VPN, not the cluster.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"context": stringProp("Kubeconfig context to use."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes get_health"},
			},
			{
				Name:        "list_namespaces",
				Description: "List namespaces with status and age.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, nil)),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_namespaces"},
			},
			{
				Name:        "list_nodes",
				Description: "List nodes with readiness, roles, version and any memory/disk/PID pressure.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, nil)),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_nodes"},
			},
			{
				Name:        "list_pods",
				Description: "List pods in a namespace with phase, readiness, restarts and per-container state. Environment variable names are reported; values never are.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"selector": stringProp("Label selector, e.g. app=web."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_pods --namespace default"},
			},
			{
				Name:        "list_workloads",
				Description: "List deployments, statefulsets and daemonsets in a namespace with desired/ready replica counts and rollout condition.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"kind": stringProp("Restrict to one of deployment, statefulset, daemonset."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_workloads --namespace default"},
			},
			{
				Name:        "list_events",
				Description: "List recent cluster events, newest last. Usually the fastest answer to why something is not starting.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultEventLimit, namespaceProps())),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_events --namespace default"},
			},
			{
				Name:        "get_logs",
				Description: "Read a bounded snapshot of a container's logs. Not a stream. Note that applications routinely log their own configuration at startup, so treat the output as sensitive.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"pod":       stringProp("Pod name."),
					"namespace": stringProp("Namespace. Empty uses the configured default."),
					"container": stringProp("Container name. Empty uses the first container."),
					"tail":      intProp(fmt.Sprintf("Lines from the end of the log. Defaults to %d, capped at %d.", DefaultLogTail, MaxLogTail)),
					"previous":  boolProp("Read the previous terminated container instead."),
					"context":   stringProp("Kubeconfig context to use."),
				}, "pod"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes get_logs --pod web-abc123"},
			},
		},
	}
}

// Manifest is what plugin.yaml embeds.
func Manifest() contract.Manifest {
	return contract.ManifestFromDefinition(Definition())
}

// pageProps are the bounding arguments every list operation carries. Every
// list here is bounded and reports truncation: a connector operation becomes an
// MCP tool, and an unbounded list against a real cluster drops a whole
// namespace into an agent's context in one result.
func pageProps(defaultLimit int, extra map[string]any) map[string]any {
	props := map[string]any{
		"limit":    intProp(fmt.Sprintf("Maximum items to return. Defaults to %d, capped at %d. The result reports truncated when more exist.", defaultLimit, MaxListLimit)),
		"continue": stringProp("Continuation token from a previous truncated result."),
		"context":  stringProp("Kubeconfig context to use."),
	}
	for k, v := range extra {
		props[k] = v
	}
	return props
}

func mergeProps(base, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func namespaceProps() map[string]any {
	return map[string]any{
		"namespace":      stringProp("Namespace to read. Empty uses the configured default."),
		"all_namespaces": boolProp("Read across every namespace instead. Deliberate rather than implied by an empty namespace."),
	}
}

func stringProp(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func intProp(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func boolProp(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}
