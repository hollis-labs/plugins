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
const Version = "0.2.0"

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
// Reads are read or read_sensitive, with no preview — making a read prompt
// for --ack would empty that gate of meaning. Every write is lifecycle, or
// destructive for delete_pod, with a server preview: the host refuses it
// without --ack, and a dry run is sent to the API server with dryRun=All.
// Finalize derives destructive, requires_ack and supports_dry from that.
// writeOperations in plugin.go is the one list of which operations write, and
// TestWriteOperationsAreExactlyTheAckGatedOnes holds the two in step.
//
// Whether a given cluster should accept these writes at all is a policy
// question about that cluster, not a property of the connector. See WP-K6 in
// docs/plans/k8s-connector-plugin.md in the Cerberus repo.
func Definition() contract.Definition {
	return contract.Finalize(contract.Definition{
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
					Description: "Namespace used when an operation names none. Empty uses the kubeconfig context's namespace, then default.",
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
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.kubeconfig", From: []string{"kubeconfig"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List kubeconfig contexts with the authentication mode each one uses. Reads no cluster and needs no credential, so it works before anything else does.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"kubeconfig": stringProp("Kubeconfig path override."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_contexts"},
			},
			{
				Name:        "check_access",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.cluster", From: []string{"context"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Report what would happen if a request were attempted: the auth mode, whether an exec credential plugin resolves from the daemon's PATH, and whether it needs a terminal there is none of. Run this first when something is wrong.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"context":    stringProp("Kubeconfig context to check."),
					"kubeconfig": stringProp("Kubeconfig path override."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes check_access"},
			},
			{
				Name:        "get_health",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.cluster", From: []string{"context"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Report API server reachability and version. Names what actually failed — a refused dial on a VPN-only endpoint is the VPN, not the cluster.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"context": stringProp("Kubeconfig context to use."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes get_health"},
			},
			{
				Name:        "list_namespaces",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.cluster", From: []string{"context"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List namespaces with status and age.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, nil)),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_namespaces"},
			},
			{
				Name:        "list_nodes",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.cluster", From: []string{"context"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List nodes with readiness, roles, version and any memory/disk/PID pressure.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, nil)),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_nodes"},
			},
			{
				Name:        "list_pods",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List pods in a namespace with phase, readiness, restarts and per-container state. Environment variable names are reported; values never are.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"selector": stringProp("Label selector, e.g. app=web."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_pods --arg namespace=default"},
			},
			{
				Name:        "list_workloads",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List deployments, statefulsets and daemonsets in a namespace with desired/ready replica counts and rollout condition.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"kind": stringProp("Restrict to one of deployment, statefulset, daemonset."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_workloads --arg namespace=default"},
			},
			{
				Name:        "list_events",
				Effect:      contract.EffectReadSensitive,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputFreeText,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List recent cluster events, newest last. Usually the fastest answer to why something is not starting.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultEventLimit, namespaceProps())),
				Examples:    []string{"cerberus connectors plugin managed exec kubernetes list_events --arg namespace=default"},
			},
			{
				Name:        "get_logs",
				Effect:      contract.EffectReadSensitive,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.pod", From: []string{"context", "namespace", "pod"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputFreeText,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Read a bounded snapshot of a container's logs. Not a stream. Note that applications routinely log their own configuration at startup, so treat the output as sensitive.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"pod":       stringProp("Pod name."),
					"namespace": stringProp("Namespace. Empty uses the configured namespace, then the kubeconfig context's, then default."),
					"container": stringProp("Container name. Empty uses the first container."),
					"tail":      intProp(fmt.Sprintf("Lines from the end of the log. Defaults to %d, capped at %d.", DefaultLogTail, MaxLogTail)),
					"previous":  boolProp("Read the previous terminated container instead."),
					"context":   stringProp("Kubeconfig context to use."),
				}, "pod"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes get_logs --arg pod=web-abc123"},
			},
			{
				Name:        "describe_workload",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.workload", From: []string{"context", "namespace", "kind", "name"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Describe one deployment, statefulset or daemonset: replica counts, conditions, rollout strategy, container images, ports, resource requests and limits, which probes are set, mounted volume names, the pods it currently owns and the events recorded against it. Environment variable names are reported; values never are.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"kind":      stringProp("One of deployment, statefulset, daemonset."),
					"name":      stringProp("Workload name."),
					"namespace": stringProp("Namespace. Empty uses the configured namespace, then the kubeconfig context's, then default."),
					"context":   stringProp("Kubeconfig context to use."),
				}, "kind", "name"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes describe_workload --arg kind=deployment --arg name=web"},
			},
			{
				Name:        "list_services",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List services with type, cluster IP, external addresses, ports and selector.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"selector": stringProp("Label selector, e.g. app=web."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_services --arg namespace=default"},
			},
			{
				Name:        "list_ingresses",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List ingresses with class, host and path routes to their backend services, and the name of each TLS secret. Secret contents are never read.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"selector": stringProp("Label selector, e.g. app=web."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_ingresses --arg all_namespaces=true"},
			},
			{
				Name:        "list_api_resources",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.cluster", From: []string{"context"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "List the resource types the API server serves, including custom resources, with their group, version, scope and verbs. Reports any API group that failed discovery by name rather than returning a silently partial list.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"group":   stringProp("Restrict to one API group, e.g. apps or cert-manager.io. Use core for the unnamed core group."),
					"limit":   intProp(fmt.Sprintf("Maximum items to return. Defaults to %d, capped at %d.", DefaultAPIResourceLimit, MaxListLimit)),
					"context": stringProp("Kubeconfig context to use."),
				}),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes list_api_resources --arg group=apps"},
			},
			{
				Name:        "top",
				Effect:      contract.EffectRead,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.namespace", From: []string{"context", "namespace"}},
				Preview:     contract.PreviewNone,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Report current CPU and memory for nodes or pods, highest CPU first. Needs metrics-server; when the cluster has none, reports available: false with the reason rather than failing.",
				InputSchema: contract.ObjectSchema(pageProps(DefaultListLimit, mergeProps(namespaceProps(), map[string]any{
					"kind":     stringProp("nodes (default) or pods."),
					"selector": stringProp("Label selector, e.g. app=web."),
				}))),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes top --arg kind=pods --arg namespace=default"},
			},

			// --- writes: acknowledgment-gated, with a server preview, every one ---
			{
				Name:        "scale_workload",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.workload", From: []string{"context", "namespace", "kind", "name"}},
				Preview:     contract.PreviewServer,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Set the replica count of a deployment or statefulset through its scale subresource. Requires --ack. --dry-run sends the change with dryRun=All, so the API server validates it, including RBAC, without applying it.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"kind":      stringProp("deployment or statefulset."),
					"name":      stringProp("Workload name."),
					"replicas":  intProp("Desired replica count, zero or more."),
					"namespace": stringProp("Namespace. Empty uses the configured namespace, then the kubeconfig context's, then default."),
					"context":   stringProp("Kubeconfig context to use."),
				}, "kind", "name", "replicas"),
				Examples: []string{
					"cerberus connectors plugin managed exec kubernetes scale_workload --arg kind=deployment --arg name=web --arg replicas=3 --dry-run --ack",
					"cerberus connectors plugin managed exec kubernetes scale_workload --arg kind=deployment --arg name=web --arg replicas=3 --ack",
				},
			},
			{
				Name:        "restart_workload",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.workload", From: []string{"context", "namespace", "kind", "name"}},
				Preview:     contract.PreviewServer,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Rolling-restart a deployment, statefulset or daemonset the way kubectl rollout restart does, by stamping the pod template; the controller replaces pods at the pace its strategy allows. Requires --ack. Supports --dry-run.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"kind":      stringProp("One of deployment, statefulset, daemonset."),
					"name":      stringProp("Workload name."),
					"namespace": stringProp("Namespace. Empty uses the configured namespace, then the kubeconfig context's, then default."),
					"context":   stringProp("Kubeconfig context to use."),
				}, "kind", "name"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes restart_workload --arg kind=deployment --arg name=web --ack"},
			},
			{
				Name:        "cordon_node",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.node", From: []string{"context", "node"}},
				Preview:     contract.PreviewServer,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Mark a node unschedulable so no new pods land on it. Pods already there keep running; this is not a drain. Requires --ack. Supports --dry-run.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"node":    stringProp("Node name."),
					"context": stringProp("Kubeconfig context to use."),
				}, "node"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes cordon_node --arg node=worker-1 --ack"},
			},
			{
				Name:        "uncordon_node",
				Effect:      contract.EffectLifecycle,
				Reversible:  true,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.node", From: []string{"context", "node"}},
				Preview:     contract.PreviewServer,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Mark a node schedulable again. Requires --ack. Supports --dry-run.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"node":    stringProp("Node name."),
					"context": stringProp("Kubeconfig context to use."),
				}, "node"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes uncordon_node --arg node=worker-1 --ack"},
			},
			{
				Name:        "delete_pod",
				Effect:      contract.EffectDestructive,
				Target:      contract.TargetDescriptor{Kind: "kubernetes.pod", From: []string{"context", "namespace", "pod"}},
				Preview:     contract.PreviewServer,
				Output:      contract.OutputStructured,
				Cost:        contract.CostNone,
				LocalFS:     contract.LocalFSNone,
				Description: "Delete one pod. Reports whether a controller owns it and will replace it, or whether the delete is permanent. Requires --ack. Supports --dry-run.",
				InputSchema: contract.ObjectSchema(map[string]any{
					"pod":                  stringProp("Pod name."),
					"namespace":            stringProp("Namespace. Empty uses the configured namespace, then the kubeconfig context's, then default."),
					"grace_period_seconds": intProp("Override the pod's termination grace period. Zero skips graceful shutdown."),
					"context":              stringProp("Kubeconfig context to use."),
				}, "pod"),
				Examples: []string{"cerberus connectors plugin managed exec kubernetes delete_pod --arg pod=web-abc123 --dry-run --ack"},
			},
		},
	})
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
		"namespace":      stringProp("Namespace to read. Empty uses the configured namespace, then the kubeconfig context's, then default."),
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
