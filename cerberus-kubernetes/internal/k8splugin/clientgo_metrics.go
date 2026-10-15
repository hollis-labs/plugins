package k8splugin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Top reads current CPU and memory from the metrics.k8s.io API.
//
// That API is served by metrics-server, which many clusters do not run — kind
// ships without it, and so do plenty of managed clusters until someone installs
// it. Its absence is a normal state, so Top reports Available: false with the
// reason instead of failing: an agent asking "what is using the CPU" should
// learn the cluster cannot say, not that the tool is broken.
//
// The answer is sorted by CPU, highest first, which is what "top" means. That
// ordering is only honest over the whole set, so up to MaxListLimit items are
// read in one page and sorted before the caller's limit is applied, rather than
// sorting a page the server chose.
func (b *clientGoBackend) Top(ctx context.Context, opts ClusterOptions, query TopQuery) (Usage, error) {
	kind := strings.ToLower(strings.TrimSpace(query.Kind))
	switch kind {
	case "", "node", "nodes":
		kind = "nodes"
	case "pod", "pods":
		kind = "pods"
	default:
		return Usage{}, fmt.Errorf("unknown top kind %q: expected nodes or pods", query.Kind)
	}

	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Usage{}, err
	}
	mc, err := b.newMetrics(cfg)
	if err != nil {
		return Usage{}, err
	}

	usage := Usage{Kind: kind, Available: true, Items: []UsageItem{}}
	// Sorted on the raw millicore value, not the rendered string.
	type ranked struct {
		item UsageItem
		cpu  int64
	}
	var items []ranked
	fetch := metav1.ListOptions{LabelSelector: query.Selector, Limit: MaxListLimit}
	var more bool

	if kind == "nodes" {
		list, err := mc.MetricsV1beta1().NodeMetricses().List(ctx, fetch)
		if err != nil {
			return metricsUnavailable(usage, err, cfg.Host)
		}
		more = list.Continue != ""

		// Percentages are against allocatable, as kubectl top reports them. A
		// failure to read nodes costs the percentages, not the answer.
		allocatable := map[string]corev1.ResourceList{}
		if nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: query.Selector, Limit: MaxListLimit}); err == nil {
			for i := range nodes.Items {
				allocatable[nodes.Items[i].Name] = nodes.Items[i].Status.Allocatable
			}
		}
		for i := range list.Items {
			m := &list.Items[i]
			cpu, mem := m.Usage[corev1.ResourceCPU], m.Usage[corev1.ResourceMemory]
			item := UsageItem{Name: m.Name, CPU: formatCPU(cpu), Memory: formatMemory(mem)}
			if alloc, ok := allocatable[m.Name]; ok {
				item.CPUPercent = percent(cpu.MilliValue(), alloc.Cpu().MilliValue())
				item.MemoryPercent = percent(mem.Value(), alloc.Memory().Value())
			}
			items = append(items, ranked{item, cpu.MilliValue()})
		}
	} else {
		namespace := query.Namespace
		if query.AllNamespaces {
			namespace = ""
		}
		usage.Namespace = namespaceScope(query.Namespace, query.AllNamespaces)
		list, err := mc.MetricsV1beta1().PodMetricses(namespace).List(ctx, fetch)
		if err != nil {
			return metricsUnavailable(usage, err, cfg.Host)
		}
		more = list.Continue != ""
		for i := range list.Items {
			m := &list.Items[i]
			var cpu, mem resource.Quantity
			for _, c := range m.Containers {
				cpu.Add(c.Usage[corev1.ResourceCPU])
				mem.Add(c.Usage[corev1.ResourceMemory])
			}
			items = append(items, ranked{UsageItem{
				Name: m.Name, Namespace: m.Namespace,
				CPU: formatCPU(cpu), Memory: formatMemory(mem),
			}, cpu.MilliValue()})
		}
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].cpu > items[j].cpu })
	limit := boundLimit(query.Limit, DefaultListLimit, MaxListLimit)
	if len(items) > limit {
		items = items[:limit]
		more = true
	}
	for _, r := range items {
		usage.Items = append(usage.Items, r.item)
	}
	usage.Count = len(usage.Items)
	usage.Truncated = more
	return usage, nil
}

// metricsUnavailable turns "this cluster serves no metrics API" into an answer
// and leaves every other failure an error. A 404 means the APIService is not
// registered — metrics-server is not installed. A 503 means it is registered
// but its backing pod is not answering, which is a different fix, so it gets a
// different reason.
func metricsUnavailable(usage Usage, err error, server string) (Usage, error) {
	switch {
	case apierrors.IsNotFound(err):
		usage.Available = false
		usage.Reason = "the cluster serves no metrics.k8s.io API; metrics-server is not installed, so current usage cannot be read"
		return usage, nil
	case apierrors.IsServiceUnavailable(err):
		usage.Available = false
		usage.Reason = "metrics.k8s.io is registered but not answering; the metrics-server pod is likely down or still starting — check it with list_pods in kube-system"
		return usage, nil
	}
	return Usage{}, fmt.Errorf("read %s metrics: %s", usage.Kind, describeError(err, server))
}

func percent(used, total int64) *int {
	if total <= 0 {
		return nil
	}
	p := int(used * 100 / total)
	return &p
}
