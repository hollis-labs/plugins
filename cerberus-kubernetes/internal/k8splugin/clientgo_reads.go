package k8splugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// DescribeWorkload reads one workload, the pods its selector matches and the
// events recorded against it. Three calls rather than one, because that is what
// "describe" means; each is bounded.
func (b *clientGoBackend) DescribeWorkload(ctx context.Context, opts ClusterOptions, ref WorkloadRef) (WorkloadDetail, error) {
	kind, err := normaliseKind(ref.Kind)
	if err != nil {
		return WorkloadDetail{}, err
	}
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return WorkloadDetail{}, err
	}

	var (
		detail   WorkloadDetail
		selector *metav1.LabelSelector
		template *corev1.PodTemplateSpec
	)
	switch kind {
	case KindDeployment:
		d, err := cs.AppsV1().Deployments(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDetail{}, fmt.Errorf("get deployment %s/%s: %s", ref.Namespace, ref.Name, describeError(err, cfg.Host))
		}
		detail.Workload = mapDeployment(d)
		detail.Strategy = string(d.Spec.Strategy.Type)
		detail.Paused = d.Spec.Paused
		detail.Conditions = deploymentConditions(d.Status.Conditions)
		selector, template = d.Spec.Selector, &d.Spec.Template
	case KindStatefulSet:
		s, err := cs.AppsV1().StatefulSets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDetail{}, fmt.Errorf("get statefulset %s/%s: %s", ref.Namespace, ref.Name, describeError(err, cfg.Host))
		}
		detail.Workload = mapStatefulSet(s)
		detail.Strategy = string(s.Spec.UpdateStrategy.Type)
		detail.Conditions = statefulSetConditions(s.Status.Conditions)
		selector, template = s.Spec.Selector, &s.Spec.Template
	case KindDaemonSet:
		d, err := cs.AppsV1().DaemonSets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDetail{}, fmt.Errorf("get daemonset %s/%s: %s", ref.Namespace, ref.Name, describeError(err, cfg.Host))
		}
		detail.Workload = mapDaemonSet(d)
		detail.Strategy = string(d.Spec.UpdateStrategy.Type)
		detail.Conditions = daemonSetConditions(d.Status.Conditions)
		selector, template = d.Spec.Selector, &d.Spec.Template
	}

	for i := range template.Spec.Containers {
		detail.Containers = append(detail.Containers, mapContainerSpec(&template.Spec.Containers[i]))
	}
	detail.Volumes = volumeRefs(template.Spec.Volumes)

	// A workload with no selector would match every pod in the namespace, which
	// is not "its pods". The API server requires one for all three kinds, so
	// this only guards against a malformed object.
	detail.Pods = newList[Pod](nil, "")
	if selector != nil {
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return WorkloadDetail{}, fmt.Errorf("parse selector for %s %s/%s: %w", kind, ref.Namespace, ref.Name, err)
		}
		detail.Selector = sel.String()
		if !sel.Empty() {
			pods, err := cs.CoreV1().Pods(ref.Namespace).List(ctx, metav1.ListOptions{
				LabelSelector: detail.Selector,
				Limit:         DescribePodLimit,
			})
			if err != nil {
				return WorkloadDetail{}, fmt.Errorf("list pods for %s %s/%s: %s", kind, ref.Namespace, ref.Name, describeError(err, cfg.Host))
			}
			out := make([]Pod, 0, len(pods.Items))
			for i := range pods.Items {
				out = append(out, mapPod(&pods.Items[i]))
			}
			detail.Pods = newList(out, pods.Continue)
		}
	}

	events, err := cs.CoreV1().Events(ref.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.kind=" + kind + ",involvedObject.name=" + ref.Name,
	})
	if err != nil {
		return WorkloadDetail{}, fmt.Errorf("list events for %s %s/%s: %s", kind, ref.Namespace, ref.Name, describeError(err, cfg.Host))
	}
	for i := range events.Items {
		// Filtered again here even though the field selector asked for it: the
		// selector is a server-side optimisation, and an event about another
		// object attributed to this one would be a wrong answer.
		obj := events.Items[i].InvolvedObject
		if obj.Kind == kind && obj.Name == ref.Name {
			detail.Events = append(detail.Events, mapEvent(&events.Items[i]))
		}
	}
	sort.SliceStable(detail.Events, func(i, j int) bool { return detail.Events[i].LastSeen < detail.Events[j].LastSeen })
	if len(detail.Events) > DescribeEventLimit {
		detail.Events = detail.Events[len(detail.Events)-DescribeEventLimit:]
	}
	return detail, nil
}

func (b *clientGoBackend) Services(ctx context.Context, opts ClusterOptions, query ScopedQuery) (List[Service], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Service]{}, err
	}
	list, err := cs.CoreV1().Services(listNamespace(query)).List(ctx, scopedListOptions(query))
	if err != nil {
		return List[Service]{}, fmt.Errorf("list services in %s: %s",
			namespaceScope(query.Namespace, query.AllNamespaces), describeError(err, cfg.Host))
	}
	out := make([]Service, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mapService(&list.Items[i]))
	}
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) Ingresses(ctx context.Context, opts ClusterOptions, query ScopedQuery) (List[Ingress], error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return List[Ingress]{}, err
	}
	list, err := cs.NetworkingV1().Ingresses(listNamespace(query)).List(ctx, scopedListOptions(query))
	if err != nil {
		return List[Ingress]{}, fmt.Errorf("list ingresses in %s: %s",
			namespaceScope(query.Namespace, query.AllNamespaces), describeError(err, cfg.Host))
	}
	out := make([]Ingress, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, mapIngress(&list.Items[i]))
	}
	return newList(out, list.Continue), nil
}

func (b *clientGoBackend) APIResources(ctx context.Context, opts ClusterOptions, query APIResourceQuery) (APIResourceList, error) {
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return APIResourceList{}, err
	}
	// ServerPreferredResources returns one version per group — the one the
	// server prefers — which is the answer to "what can I ask for" without
	// listing every deprecated version of every type.
	groups, err := cs.Discovery().ServerPreferredResourcesWithContext(ctx)
	var failed []string
	if err != nil {
		// A partial failure still returns every group that answered. Report the
		// missing groups by name rather than failing the whole read: one
		// aggregated API server being down is the common case, and it is the
		// thing an operator is probably trying to diagnose.
		var groupErr *discovery.ErrGroupDiscoveryFailed
		if !errors.As(err, &groupErr) {
			return APIResourceList{}, fmt.Errorf("discover API resources: %s", describeError(err, cfg.Host))
		}
		for gv := range groupErr.Groups {
			failed = append(failed, gv.String())
		}
		sort.Strings(failed)
	}

	return mapAPIResources(groups, failed, query), nil
}

// mapAPIResources is separate from discovery so it can be tested: client-go's
// fake discovery returns nothing from ServerPreferredResources.
func mapAPIResources(groups []*metav1.APIResourceList, failed []string, query APIResourceQuery) APIResourceList {
	want := strings.TrimSpace(query.Group)
	if strings.EqualFold(want, "core") {
		want = ""
	}
	filtering := strings.TrimSpace(query.Group) != ""

	var out []APIResource
	for _, group := range groups {
		gv, err := schema.ParseGroupVersion(group.GroupVersion)
		if err != nil {
			continue
		}
		if filtering && gv.Group != want {
			continue
		}
		for _, r := range group.APIResources {
			// Subresources — pods/log, deployments/scale — are not types an
			// operator lists, and would roughly double the answer.
			if strings.Contains(r.Name, "/") {
				continue
			}
			out = append(out, APIResource{
				Name:       r.Name,
				Kind:       r.Kind,
				Group:      gv.Group,
				Version:    gv.Version,
				Namespaced: r.Namespaced,
				Verbs:      append([]string(nil), r.Verbs...),
				ShortNames: append([]string(nil), r.ShortNames...),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})

	limit := boundLimit(query.Limit, DefaultAPIResourceLimit, MaxListLimit)
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	result := APIResourceList{List: newList(out, ""), FailedGroups: failed}
	// Discovery has no continuation token; the result is sorted, so a caller
	// narrows with group rather than paging.
	result.Truncated = truncated
	return result
}

// --- mapping ---

func listNamespace(query ScopedQuery) string {
	if query.AllNamespaces {
		return ""
	}
	return query.Namespace
}

func scopedListOptions(query ScopedQuery) metav1.ListOptions {
	return metav1.ListOptions{
		LabelSelector: query.Selector,
		Limit:         int64(boundLimit(query.Limit, DefaultListLimit, MaxListLimit)),
		Continue:      query.Continue,
	}
}

func mapContainerSpec(c *corev1.Container) ContainerSpec {
	out := ContainerSpec{
		Name:     c.Name,
		Image:    c.Image,
		Requests: quantities(c.Resources.Requests),
		Limits:   quantities(c.Resources.Limits),
	}
	for _, p := range c.Ports {
		port := fmt.Sprintf("%d/%s", p.ContainerPort, protocol(p.Protocol))
		if p.Name != "" {
			port = p.Name + ":" + port
		}
		out.Ports = append(out.Ports, port)
	}
	if c.LivenessProbe != nil {
		out.Probes = append(out.Probes, "liveness")
	}
	if c.ReadinessProbe != nil {
		out.Probes = append(out.Probes, "readiness")
	}
	if c.StartupProbe != nil {
		out.Probes = append(out.Probes, "startup")
	}
	// Names, never values — the same rule as mapPod, for the same reason.
	for _, env := range c.Env {
		out.EnvNames = append(out.EnvNames, env.Name)
	}
	for _, from := range c.EnvFrom {
		switch {
		case from.SecretRef != nil:
			out.EnvFromNames = append(out.EnvFromNames, "secret/"+from.SecretRef.Name)
		case from.ConfigMapRef != nil:
			out.EnvFromNames = append(out.EnvFromNames, "configmap/"+from.ConfigMapRef.Name)
		}
	}
	return out
}

func quantities(list corev1.ResourceList) map[string]string {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	for name, q := range list {
		out[string(name)] = q.String()
	}
	return out
}

func protocol(p corev1.Protocol) string {
	if p == "" {
		return string(corev1.ProtocolTCP)
	}
	return string(p)
}

// volumeRefs renders each volume as "kind/name". The kinds that reference
// another object carry that object's name; the rest carry the volume's own.
func volumeRefs(volumes []corev1.Volume) []string {
	var out []string
	for _, v := range volumes {
		switch {
		case v.Secret != nil:
			out = append(out, "secret/"+v.Secret.SecretName)
		case v.ConfigMap != nil:
			out = append(out, "configmap/"+v.ConfigMap.Name)
		case v.PersistentVolumeClaim != nil:
			out = append(out, "pvc/"+v.PersistentVolumeClaim.ClaimName)
		case v.EmptyDir != nil:
			out = append(out, "emptyDir/"+v.Name)
		case v.HostPath != nil:
			// The host path itself, because a hostPath mount is exactly the
			// thing an operator auditing a workload wants to see.
			out = append(out, "hostPath/"+v.HostPath.Path)
		case v.Projected != nil:
			out = append(out, "projected/"+v.Name)
		default:
			out = append(out, "other/"+v.Name)
		}
	}
	return out
}

func deploymentConditions(conds []appsv1.DeploymentCondition) []Condition {
	out := make([]Condition, 0, len(conds))
	for _, c := range conds {
		out = append(out, Condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			LastTransition: timestamp(c.LastTransitionTime),
		})
	}
	return out
}

func statefulSetConditions(conds []appsv1.StatefulSetCondition) []Condition {
	out := make([]Condition, 0, len(conds))
	for _, c := range conds {
		out = append(out, Condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			LastTransition: timestamp(c.LastTransitionTime),
		})
	}
	return out
}

func daemonSetConditions(conds []appsv1.DaemonSetCondition) []Condition {
	out := make([]Condition, 0, len(conds))
	for _, c := range conds {
		out = append(out, Condition{
			Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message,
			LastTransition: timestamp(c.LastTransitionTime),
		})
	}
	return out
}

func timestamp(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func mapService(svc *corev1.Service) Service {
	out := Service{
		Name:      svc.Name,
		Namespace: svc.Namespace,
		Type:      string(svc.Spec.Type),
		ClusterIP: svc.Spec.ClusterIP,
		Age:       age(svc.CreationTimestamp),
	}
	out.ExternalAddresses = append(out.ExternalAddresses, svc.Spec.ExternalIPs...)
	for _, ing := range svc.Status.LoadBalancer.Ingress {
		switch {
		case ing.Hostname != "":
			out.ExternalAddresses = append(out.ExternalAddresses, ing.Hostname)
		case ing.IP != "":
			out.ExternalAddresses = append(out.ExternalAddresses, ing.IP)
		}
	}
	if svc.Spec.Type == corev1.ServiceTypeExternalName && svc.Spec.ExternalName != "" {
		out.ExternalAddresses = append(out.ExternalAddresses, svc.Spec.ExternalName)
	}
	for _, p := range svc.Spec.Ports {
		port := fmt.Sprintf("%d→%s/%s", p.Port, p.TargetPort.String(), protocol(p.Protocol))
		if p.Name != "" {
			port = p.Name + " " + port
		}
		if p.NodePort != 0 {
			port += fmt.Sprintf(":%d", p.NodePort)
		}
		out.Ports = append(out.Ports, port)
	}
	if len(svc.Spec.Selector) > 0 {
		out.Selector = labels.SelectorFromSet(svc.Spec.Selector).String()
	}
	return out
}

func mapIngress(ing *networkingv1.Ingress) Ingress {
	out := Ingress{
		Name:      ing.Name,
		Namespace: ing.Namespace,
		Age:       age(ing.CreationTimestamp),
	}
	if ing.Spec.IngressClassName != nil {
		out.Class = *ing.Spec.IngressClassName
	}
	if def := ing.Spec.DefaultBackend; def != nil {
		out.Routes = append(out.Routes, "* → "+ingressBackend(def))
	}
	for _, rule := range ing.Spec.Rules {
		host := rule.Host
		if host == "" {
			host = "*"
		}
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			out.Routes = append(out.Routes, host+path.Path+" → "+ingressBackend(&path.Backend))
		}
	}
	for _, tls := range ing.Spec.TLS {
		// The secret's name, never its contents: it holds the private key.
		tlsRef := IngressTLS{Hosts: append([]string(nil), tls.Hosts...)}
		if tls.SecretName != "" {
			tlsRef.Certificate = "secret/" + tls.SecretName
		}
		out.TLS = append(out.TLS, tlsRef)
	}
	for _, lb := range ing.Status.LoadBalancer.Ingress {
		switch {
		case lb.Hostname != "":
			out.Addresses = append(out.Addresses, lb.Hostname)
		case lb.IP != "":
			out.Addresses = append(out.Addresses, lb.IP)
		}
	}
	return out
}

func ingressBackend(b *networkingv1.IngressBackend) string {
	switch {
	case b.Service != nil:
		port := b.Service.Port.Name
		if port == "" {
			port = fmt.Sprintf("%d", b.Service.Port.Number)
		}
		return b.Service.Name + ":" + port
	case b.Resource != nil:
		return b.Resource.Kind + "/" + b.Resource.Name
	default:
		return "unknown"
	}
}

// formatCPU and formatMemory render quantities the way kubectl top does: CPU in
// millicores, memory in Mi.
func formatCPU(q resource.Quantity) string {
	return fmt.Sprintf("%dm", q.MilliValue())
}

func formatMemory(q resource.Quantity) string {
	return fmt.Sprintf("%dMi", q.Value()/(1024*1024))
}
