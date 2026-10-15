package k8splugin

import (
	"context"
	"fmt"
	"strings"
)

// Backend is the seam that keeps client-go swappable and the plugin testable
// without a cluster. `digitalocean`, `docker` and `contextforge` all use this
// shape.
//
// Everything above this interface speaks in our own DTOs. No Kubernetes type
// appears in a signature here, which is what makes the ADR 0003 boundary
// checkable by reading one file.
//
// list_contexts and check_access are deliberately absent: both are answered
// from the kubeconfig with no cluster contact, by Contexts and Preflight in
// auth.go, so they keep working when the backend cannot connect at all.
type Backend interface {
	Health(ctx context.Context, opts ClusterOptions) (Health, error)
	Namespaces(ctx context.Context, opts ClusterOptions, query ListQuery) (List[Namespace], error)
	Nodes(ctx context.Context, opts ClusterOptions, query ListQuery) (List[Node], error)
	Pods(ctx context.Context, opts ClusterOptions, query PodQuery) (List[Pod], error)
	Workloads(ctx context.Context, opts ClusterOptions, query WorkloadQuery) (List[Workload], error)
	Events(ctx context.Context, opts ClusterOptions, query EventQuery) (List[Event], error)
	Logs(ctx context.Context, opts ClusterOptions, query LogQuery) (LogSnapshot, error)
	DescribeWorkload(ctx context.Context, opts ClusterOptions, ref WorkloadRef) (WorkloadDetail, error)
	Services(ctx context.Context, opts ClusterOptions, query ScopedQuery) (List[Service], error)
	Ingresses(ctx context.Context, opts ClusterOptions, query ScopedQuery) (List[Ingress], error)
	APIResources(ctx context.Context, opts ClusterOptions, query APIResourceQuery) (APIResourceList, error)
	Top(ctx context.Context, opts ClusterOptions, query TopQuery) (Usage, error)

	// Writes. Every one takes a DryRun flag that is passed to the API server as
	// dryRun=All rather than simulated here, and returns a Change built from an
	// allow-list of fields. See dto_writes.go.
	Scale(ctx context.Context, opts ClusterOptions, req ScaleRequest) (Change, error)
	Restart(ctx context.Context, opts ClusterOptions, req RestartRequest) (Change, error)
	SetSchedulable(ctx context.Context, opts ClusterOptions, req SchedulableRequest) (Change, error)
	DeletePod(ctx context.Context, opts ClusterOptions, req DeletePodRequest) (Change, error)
}

// List is a bounded page of results.
//
// Every list operation here is bounded, and says so when it truncates. This is
// not tidiness: a connector operation becomes an MCP tool, and an unbounded
// list against a real cluster puts every pod in a namespace into an agent's
// context window in one result. Silent truncation is worse still — an agent
// that cannot tell "three pods" from "the first three of nine hundred" will
// reason confidently from a partial answer.
//
// Continue is the API server's own continuation token, passed back out so a
// caller that genuinely wants the next page can ask for it rather than raising
// the limit until the response fits.
type List[T any] struct {
	Items     []T    `json:"items"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated,omitempty"`
	Continue  string `json:"continue,omitempty"`
}

// newList wraps items and records whether the API server had more to give.
func newList[T any](items []T, continueToken string) List[T] {
	if items == nil {
		items = []T{}
	}
	return List[T]{
		Items:     items,
		Count:     len(items),
		Truncated: continueToken != "",
		Continue:  continueToken,
	}
}

// ListQuery bounds a listing that needs no other scoping.
type ListQuery struct {
	Limit    int
	Continue string
}

// PodQuery scopes a pod listing.
type PodQuery struct {
	// Namespace is ignored when AllNamespaces is set. An empty namespace on the
	// wire means "the configured default", not "every namespace" — asking for
	// the whole cluster has to be deliberate.
	Namespace     string
	AllNamespaces bool
	Selector      string
	Limit         int
	Continue      string
}

// WorkloadQuery scopes a workload listing. An empty Kind returns all three.
type WorkloadQuery struct {
	Namespace     string
	AllNamespaces bool
	Kind          string
	Limit         int
}

// EventQuery scopes an event listing.
type EventQuery struct {
	Namespace     string
	AllNamespaces bool
	Limit         int
	Continue      string
}

// LogQuery scopes a log read. There is no Follow: the admin lane returns a
// result, not a stream.
type LogQuery struct {
	Namespace string
	Pod       string
	Container string
	Tail      int
	Previous  bool
}

// ScopedQuery scopes a namespaced listing that supports label selection.
type ScopedQuery struct {
	Namespace     string
	AllNamespaces bool
	Selector      string
	Limit         int
	Continue      string
}

// WorkloadRef names one workload. Kind is normalised by normaliseKind.
type WorkloadRef struct {
	Namespace string
	Kind      string
	Name      string
}

// APIResourceQuery filters discovery. Group matches exactly, with "core" as an
// alias for the unnamed core group.
type APIResourceQuery struct {
	Group string
	Limit int
}

// TopQuery scopes a metrics read. Kind is "nodes" or "pods"; pods are
// namespace-scoped like every other pod read.
type TopQuery struct {
	Kind          string
	Namespace     string
	AllNamespaces bool
	Selector      string
	Limit         int
}

// ScaleRequest sets a Deployment's or StatefulSet's replica count. A DaemonSet
// has no replica count to set.
type ScaleRequest struct {
	WorkloadRef
	Replicas int32
	DryRun   bool
}

// RestartRequest triggers a rolling restart the way `kubectl rollout restart`
// does: by stamping the pod template, so the controller replaces pods at the
// pace its rollout strategy allows rather than all at once.
type RestartRequest struct {
	WorkloadRef
	DryRun bool
}

// SchedulableRequest cordons (Schedulable false) or uncordons a node. It does
// not drain: pods already on the node stay there.
type SchedulableRequest struct {
	Node        string
	Schedulable bool
	DryRun      bool
}

// DeletePodRequest deletes one pod. GracePeriod nil uses the pod's own
// terminationGracePeriodSeconds.
type DeletePodRequest struct {
	Namespace   string
	Name        string
	GracePeriod *int64
	DryRun      bool
}

// Workload kinds, in their canonical Kubernetes spelling.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
)

// normaliseKind accepts the spellings an operator types — "deploy",
// "deployments", "sts", "DaemonSet" — and returns the canonical kind, or an
// error naming the accepted set. An unrecognised kind is a typo, and a typo that
// silently matched nothing would read as an empty namespace.
func normaliseKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "deployments", "deploy":
		return KindDeployment, nil
	case "statefulset", "statefulsets", "sts":
		return KindStatefulSet, nil
	case "daemonset", "daemonsets", "ds":
		return KindDaemonSet, nil
	default:
		return "", fmt.Errorf("unknown workload kind %q: expected one of deployment, statefulset, daemonset", kind)
	}
}

// Defaults applied when an operation omits them. They are deliberately small:
// an operator who wants more can ask, and an agent that gets a truncated flag
// knows to narrow its query rather than widen the window.
const (
	DefaultListLimit  = 100
	MaxListLimit      = 1000
	DefaultEventLimit = 50
	DefaultLogTail    = 200
	MaxLogTail        = 5000
	DefaultNamespace  = "default"

	// describe_workload embeds bounded pod and event lists; a workload with
	// hundreds of replicas should not turn one describe into a namespace dump.
	DescribePodLimit   = 20
	DescribeEventLimit = 20

	// Discovery on a cluster with many CRDs runs to several hundred types.
	DefaultAPIResourceLimit = 200
)

// boundLimit clamps a caller-supplied limit into something a tool result can
// carry. A caller asking for everything gets MaxListLimit and a truncated flag,
// not a response nothing can read.
func boundLimit(requested, fallback, max int) int {
	if requested <= 0 {
		return fallback
	}
	if requested > max {
		return max
	}
	return requested
}

// AllNamespacesScope renders the namespace a listing actually used, for the
// DTO and for error messages. "*" rather than an empty string, because an empty
// string in an error reads like a bug.
func namespaceScope(namespace string, all bool) string {
	if all {
		return "*"
	}
	return namespace
}
