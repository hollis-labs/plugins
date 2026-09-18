package k8splugin

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// clientGoBackend is the only file in this package that imports client-go. The
// ADR 0003 boundary is therefore checkable by reading the import block of every
// other file: if a Kubernetes type appears outside here, the DTO layer has
// been bypassed.
// clientsetFactory resolves a connection. It is a field rather than a direct
// call so tests can inject client-go's own fake clientset and drive every
// method below against real typed objects — the mapping is most of the risk
// here and it does not need a cluster to exercise.
type clientsetFactory func(ClusterOptions) (kubernetes.Interface, *rest.Config, error)

type clientGoBackend struct {
	newClientset clientsetFactory
}

var _ Backend = (*clientGoBackend)(nil)

// NewClientGoBackend builds the live backend. It holds no client and no
// credential: every operation resolves its own connection, because boot-time
// resolution is what let the Docker connector cache a failure for the daemon's
// lifetime while reporting healthy.
func NewClientGoBackend() Backend { return &clientGoBackend{newClientset: liveClientset} }

// newFakeClientGoBackend drives the real mapping code from an injected
// clientset. Test-only, but it lives here rather than in a _test.go file so the
// factory field has exactly one meaning.
func newClientGoBackendWith(factory clientsetFactory) Backend {
	return &clientGoBackend{newClientset: factory}
}

func liveClientset(opts ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
	cfg, _, err := RestConfig(opts)
	if err != nil {
		return nil, nil, err
	}
	// A short default keeps a dead VPN from hanging an operator's CLI for the
	// full TCP timeout.
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return cs, cfg, nil
}

func (b *clientGoBackend) clientset(opts ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
	return b.newClientset(opts)
}

func (b *clientGoBackend) Health(ctx context.Context, opts ClusterOptions) (Health, error) {
	name := opts.Context
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Health{Reachable: false, Context: name, Message: err.Error()}, nil
	}
	version, err := cs.Discovery().ServerVersion()
	if err != nil {
		return Health{
			Reachable: false,
			Context:   name,
			Server:    cfg.Host,
			Message:   describeError(err, cfg.Host),
		}, nil
	}
	_ = ctx
	return Health{
		Reachable: true,
		Context:   name,
		Server:    cfg.Host,
		Version:   version.GitVersion,
		Platform:  version.Platform,
	}, nil
}

func (b *clientGoBackend) Namespaces(ctx context.Context, opts ClusterOptions, query ListQuery) (List[Namespace], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Namespace]{}, err
	}
	list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		Limit:    int64(boundLimit(query.Limit, DefaultListLimit, MaxListLimit)),
		Continue: query.Continue,
	})
	if err != nil {
		return List[Namespace]{}, fmt.Errorf("list namespaces: %s", describeError(err, cfg.Host))
	}
	out := make([]Namespace, 0, len(list.Items))
	for i := range list.Items {
		ns := &list.Items[i]
		out = append(out, Namespace{
			Name:    ns.Name,
			Status:  string(ns.Status.Phase),
			Age:     age(ns.CreationTimestamp),
			Created: ns.CreationTimestamp.UTC().Format(time.RFC3339),
		})
	}
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) Nodes(ctx context.Context, opts ClusterOptions, query ListQuery) (List[Node], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Node]{}, err
	}
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		Limit:    int64(boundLimit(query.Limit, DefaultListLimit, MaxListLimit)),
		Continue: query.Continue,
	})
	if err != nil {
		return List[Node]{}, fmt.Errorf("list nodes: %s", describeError(err, cfg.Host))
	}
	out := make([]Node, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mapNode(&list.Items[i]))
	}
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) Pods(ctx context.Context, opts ClusterOptions, query PodQuery) (List[Pod], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Pod]{}, err
	}
	// An empty namespace is how client-go spells "every namespace", so it is
	// only ever passed when AllNamespaces was asked for explicitly.
	namespace := query.Namespace
	if query.AllNamespaces {
		namespace = ""
	}
	list, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: query.Selector,
		Limit:         int64(boundLimit(query.Limit, DefaultListLimit, MaxListLimit)),
		Continue:      query.Continue,
	})
	if err != nil {
		return List[Pod]{}, fmt.Errorf("list pods in %s: %s",
			namespaceScope(query.Namespace, query.AllNamespaces), describeError(err, cfg.Host))
	}
	out := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mapPod(&list.Items[i]))
	}
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) Workloads(ctx context.Context, opts ClusterOptions, query WorkloadQuery) (List[Workload], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Workload]{}, err
	}
	namespace := query.Namespace
	if query.AllNamespaces {
		namespace = ""
	}
	scope := namespaceScope(query.Namespace, query.AllNamespaces)
	// Lower-case before trimming the plural, or an upper-case "DEPLOYMENTS"
	// keeps its S and reads as an unknown kind.
	kind := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(query.Kind)), "s")

	// One budget across all three kinds, not one each: the caller asked for a
	// bounded answer, and three kinds silently returning 3x the limit is the
	// same unbounded-result problem wearing a smaller number.
	remaining := boundLimit(query.Limit, DefaultListLimit, MaxListLimit)
	var out []Workload
	var truncated bool

	page := func() metav1.ListOptions {
		return metav1.ListOptions{Limit: int64(remaining)}
	}

	if remaining > 0 && (kind == "" || kind == "deployment") {
		list, err := cs.AppsV1().Deployments(namespace).List(ctx, page())
		if err != nil {
			return List[Workload]{}, fmt.Errorf("list deployments in %s: %s", scope, describeError(err, cfg.Host))
		}
		for i := range list.Items {
			out = append(out, mapDeployment(&list.Items[i]))
		}
		remaining -= len(list.Items)
		truncated = truncated || list.Continue != ""
	}
	if remaining > 0 && (kind == "" || kind == "statefulset") {
		list, err := cs.AppsV1().StatefulSets(namespace).List(ctx, page())
		if err != nil {
			return List[Workload]{}, fmt.Errorf("list statefulsets in %s: %s", scope, describeError(err, cfg.Host))
		}
		for i := range list.Items {
			out = append(out, mapStatefulSet(&list.Items[i]))
		}
		remaining -= len(list.Items)
		truncated = truncated || list.Continue != ""
	}
	if remaining > 0 && (kind == "" || kind == "daemonset") {
		list, err := cs.AppsV1().DaemonSets(namespace).List(ctx, page())
		if err != nil {
			return List[Workload]{}, fmt.Errorf("list daemonsets in %s: %s", scope, describeError(err, cfg.Host))
		}
		for i := range list.Items {
			out = append(out, mapDaemonSet(&list.Items[i]))
		}
		remaining -= len(list.Items)
		truncated = truncated || list.Continue != ""
	}

	// A kind filter that matched nothing is a typo, not an empty cluster.
	if kind != "" && kind != "deployment" && kind != "statefulset" && kind != "daemonset" {
		return List[Workload]{}, fmt.Errorf("unknown workload kind %q: expected one of deployment, statefulset, daemonset", query.Kind)
	}

	result := newList(out, "")
	// Truncation here cannot hand back a usable continue token, because it spans
	// three separate paginations. Say so rather than imply a page can be resumed.
	result.Truncated = truncated || remaining <= 0
	return result, nil
}

func (b *clientGoBackend) Events(ctx context.Context, opts ClusterOptions, query EventQuery) (List[Event], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Event]{}, err
	}
	namespace := query.Namespace
	if query.AllNamespaces {
		namespace = ""
	}
	list, err := cs.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
		Limit:    int64(boundLimit(query.Limit, DefaultEventLimit, MaxListLimit)),
		Continue: query.Continue,
	})
	if err != nil {
		return List[Event]{}, fmt.Errorf("list events in %s: %s",
			namespaceScope(query.Namespace, query.AllNamespaces), describeError(err, cfg.Host))
	}
	out := make([]Event, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mapEvent(&list.Items[i]))
	}
	// Newest last: an operator reading a terminal wants the most recent line at
	// the bottom, which is also where an agent reading a truncated result looks.
	sort.SliceStable(out, func(i, j int) bool { return out[i].LastSeen < out[j].LastSeen })
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) Logs(ctx context.Context, opts ClusterOptions, query LogQuery) (LogSnapshot, error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return LogSnapshot{}, err
	}
	tail := boundLimit(query.Tail, DefaultLogTail, MaxLogTail)
	tail64 := int64(tail)
	req := cs.CoreV1().Pods(query.Namespace).GetLogs(query.Pod, &corev1.PodLogOptions{
		Container: query.Container,
		TailLines: &tail64,
		Previous:  query.Previous,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return LogSnapshot{}, fmt.Errorf("read logs for %s/%s: %s", query.Namespace, query.Pod, describeError(err, cfg.Host))
	}
	defer func() { _ = stream.Close() }()

	snapshot := LogSnapshot{
		Namespace: query.Namespace,
		Pod:       query.Pod,
		Container: query.Container,
		Previous:  query.Previous,
		Lines:     []string{},
	}
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		snapshot.Lines = append(snapshot.Lines, scanner.Text())
		if len(snapshot.Lines) >= tail {
			snapshot.Truncated = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return snapshot, fmt.Errorf("read logs for %s/%s: %w", query.Namespace, query.Pod, err)
	}
	return snapshot, nil
}

// --- mapping ---
//
// Explicit, hand-written mapping rather than codegen. ADR 0003's rule: codegen
// when the DTO exists to *reshape* something, explicit mapping when it exists
// to *exclude* something. These exist to exclude.

func mapNode(node *corev1.Node) Node {
	out := Node{
		Name:          node.Name,
		Version:       node.Status.NodeInfo.KubeletVersion,
		OS:            node.Status.NodeInfo.OperatingSystem,
		Arch:          node.Status.NodeInfo.Architecture,
		Unschedulable: node.Spec.Unschedulable,
		Age:           age(node.CreationTimestamp),
	}
	for _, cond := range node.Status.Conditions {
		switch {
		case cond.Type == corev1.NodeReady:
			out.Ready = cond.Status == corev1.ConditionTrue
			out.Status = string(cond.Type) + "=" + string(cond.Status)
		case cond.Status == corev1.ConditionTrue:
			out.Pressures = append(out.Pressures, string(cond.Type))
		}
	}
	// Roles come from node-role.kubernetes.io/<role> label keys — key names
	// only, which is all a role is.
	for key := range node.Labels {
		if role, ok := strings.CutPrefix(key, "node-role.kubernetes.io/"); ok && role != "" {
			out.Roles = append(out.Roles, role)
		}
	}
	sort.Strings(out.Roles)
	return out
}

func mapPod(pod *corev1.Pod) Pod {
	out := Pod{
		Name:      pod.Name,
		Namespace: pod.Namespace,
		Phase:     string(pod.Status.Phase),
		Node:      pod.Spec.NodeName,
		Age:       age(pod.CreationTimestamp),
	}
	if len(pod.OwnerReferences) > 0 {
		out.Owner = pod.OwnerReferences[0].Kind + "/" + pod.OwnerReferences[0].Name
	}

	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.ContainerStatuses))
	for _, cs := range pod.Status.ContainerStatuses {
		statuses[cs.Name] = cs
	}

	ready := 0
	for i := range pod.Spec.Containers {
		spec := &pod.Spec.Containers[i]
		container := Container{Name: spec.Name, Image: spec.Image}

		// Names, never values. Spec.Containers[].Env is, in practice, where
		// application credentials live; emitting it would put them in CLI
		// stdout, daemon logs, MCP results and an agent's context at once.
		for _, env := range spec.Env {
			container.EnvNames = append(container.EnvNames, env.Name)
		}
		for _, from := range spec.EnvFrom {
			switch {
			case from.SecretRef != nil:
				container.EnvFromNames = append(container.EnvFromNames, "secret/"+from.SecretRef.Name)
			case from.ConfigMapRef != nil:
				container.EnvFromNames = append(container.EnvFromNames, "configmap/"+from.ConfigMapRef.Name)
			}
		}

		if cs, ok := statuses[spec.Name]; ok {
			container.Ready = cs.Ready
			container.RestartCount = cs.RestartCount
			container.State, container.Reason, container.Message = containerState(cs.State)
			out.Restarts += cs.RestartCount
			if cs.Ready {
				ready++
			}
		}
		out.Containers = append(out.Containers, container)
	}
	out.Ready = fmt.Sprintf("%d/%d", ready, len(pod.Spec.Containers))
	return out
}

func containerState(state corev1.ContainerState) (name, reason, message string) {
	switch {
	case state.Running != nil:
		return "running", "", ""
	case state.Waiting != nil:
		return "waiting", state.Waiting.Reason, state.Waiting.Message
	case state.Terminated != nil:
		return "terminated", state.Terminated.Reason, state.Terminated.Message
	default:
		return "unknown", "", ""
	}
}

func mapDeployment(d *appsv1.Deployment) Workload {
	out := Workload{
		Kind:      "Deployment",
		Name:      d.Name,
		Namespace: d.Namespace,
		Ready:     d.Status.ReadyReplicas,
		Updated:   d.Status.UpdatedReplicas,
		Available: d.Status.AvailableReplicas,
		Images:    podImages(&d.Spec.Template),
		Age:       age(d.CreationTimestamp),
	}
	if d.Spec.Replicas != nil {
		out.Desired = *d.Spec.Replicas
	}
	for _, cond := range d.Status.Conditions {
		if cond.Status == corev1.ConditionTrue {
			out.Condition = string(cond.Type)
			if cond.Reason != "" {
				out.Condition += ": " + cond.Reason
			}
		}
	}
	return out
}

func mapStatefulSet(s *appsv1.StatefulSet) Workload {
	out := Workload{
		Kind:      "StatefulSet",
		Name:      s.Name,
		Namespace: s.Namespace,
		Ready:     s.Status.ReadyReplicas,
		Updated:   s.Status.UpdatedReplicas,
		Available: s.Status.AvailableReplicas,
		Images:    podImages(&s.Spec.Template),
		Age:       age(s.CreationTimestamp),
	}
	if s.Spec.Replicas != nil {
		out.Desired = *s.Spec.Replicas
	}
	return out
}

func mapDaemonSet(d *appsv1.DaemonSet) Workload {
	return Workload{
		Kind:      "DaemonSet",
		Name:      d.Name,
		Namespace: d.Namespace,
		Desired:   d.Status.DesiredNumberScheduled,
		Ready:     d.Status.NumberReady,
		Updated:   d.Status.UpdatedNumberScheduled,
		Available: d.Status.NumberAvailable,
		Images:    podImages(&d.Spec.Template),
		Age:       age(d.CreationTimestamp),
	}
}

func podImages(template *corev1.PodTemplateSpec) []string {
	images := make([]string, 0, len(template.Spec.Containers))
	for i := range template.Spec.Containers {
		images = append(images, template.Spec.Containers[i].Image)
	}
	return images
}

func mapEvent(event *corev1.Event) Event {
	out := Event{
		Namespace: event.Namespace,
		Type:      event.Type,
		Reason:    event.Reason,
		Message:   event.Message,
		Count:     event.Count,
	}
	if event.InvolvedObject.Kind != "" {
		out.Object = event.InvolvedObject.Kind + "/" + event.InvolvedObject.Name
	}
	if !event.FirstTimestamp.IsZero() {
		out.FirstSeen = event.FirstTimestamp.UTC().Format(time.RFC3339)
	}
	// LastTimestamp is empty on events written through the newer events.k8s.io
	// path, which carry EventTime instead. Falling back keeps the sort in
	// Events() meaningful on a cluster that emits both.
	last := event.LastTimestamp.Time
	if last.IsZero() {
		last = event.EventTime.Time
	}
	if !last.IsZero() {
		out.LastSeen = last.UTC().Format(time.RFC3339)
	}
	return out
}

func age(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return duration.HumanDuration(time.Since(t.Time))
}

// describeError names the thing that actually failed. A refused connection on a
// VPN-only API server means the VPN is down, not that the cluster is unhealthy,
// and telling an operator the wrong one sends them to the wrong system. The
// strings are composed from fixed text plus a server URL and the API server's
// own message — nothing token-shaped — so the host's redaction has nothing to
// eat on the way out.
func describeError(err error, server string) string {
	if err == nil {
		return ""
	}
	switch {
	case apierrors.IsUnauthorized(err):
		return fmt.Sprintf("the API server at %s rejected our identity; the credential is missing or expired — run check_access to see which authentication mode this context uses and refresh it", server)
	case apierrors.IsForbidden(err):
		return fmt.Sprintf("authenticated but not permitted: %s — this connector is read-only by design, and a missing read permission is an RBAC grant to request from whoever owns the cluster", err.Error())
	case apierrors.IsNotFound(err):
		return err.Error()
	case apierrors.IsTimeout(err), errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("timed out talking to the API server at %s; if the cluster is only reachable on the VPN, check the VPN before the cluster", server)
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return fmt.Sprintf("cannot resolve the API server host in %s; if the cluster is only reachable on the VPN, check the VPN before the cluster", server)
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return fmt.Sprintf("cannot reach the API server at %s: %s — if the cluster is only reachable on the VPN, check the VPN before the cluster", server, opErr.Err)
		}
	}

	// An exec credential plugin that failed reports through here. Name it,
	// because "Unauthorized" would send an operator to the wrong problem.
	if text := err.Error(); strings.Contains(text, "exec") && strings.Contains(text, "credential") {
		return fmt.Sprintf("credential_missing: the exec credential plugin for this context failed: %s — run check_access to see whether it resolves from the daemon PATH", text)
	}
	return err.Error()
}
