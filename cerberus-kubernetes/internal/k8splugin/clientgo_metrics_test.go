package k8splugin

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func metricsBackend(core []runtime.Object, metrics *metricsfake.Clientset) Backend {
	clientset := fake.NewClientset(core...)
	return newClientGoBackendWithMetrics(
		func(ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
			return clientset, &rest.Config{Host: "https://fake.example.com"}, nil
		},
		func(*rest.Config) (metricsclient.Interface, error) { return metrics, nil },
	)
}

// servingMetrics returns a metrics fake that answers list with the given
// objects. The generated fake's tracker files NodeMetrics under the guessed
// plural "nodemetricses" while the client lists "nodes", so objects seeded
// the usual way are never found; a reactor sidesteps the mismatch.
func servingMetrics(nodes []metricsv1beta1.NodeMetrics, pods []metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	metrics := metricsfake.NewSimpleClientset()
	metrics.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.NodeMetricsList{Items: nodes}, nil
	})
	metrics.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.PodMetricsList{Items: pods}, nil
	})
	return metrics
}

func usage(cpu, mem string) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(cpu),
		corev1.ResourceMemory: resource.MustParse(mem),
	}
}

func TestTopNodesSortsByCPUAndReportsPercentOfAllocatable(t *testing.T) {
	nodes := []runtime.Object{
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "quiet"}, Status: corev1.NodeStatus{Allocatable: usage("2", "4Gi")}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "busy"}, Status: corev1.NodeStatus{Allocatable: usage("2", "4Gi")}},
	}
	metrics := servingMetrics([]metricsv1beta1.NodeMetrics{
		{ObjectMeta: metav1.ObjectMeta{Name: "quiet"}, Usage: usage("100m", "1Gi")},
		{ObjectMeta: metav1.ObjectMeta{Name: "busy"}, Usage: usage("1500m", "2Gi")},
	}, nil)

	got, err := metricsBackend(nodes, metrics).Top(context.Background(), ClusterOptions{}, TopQuery{Kind: "nodes"})
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if !got.Available || got.Count != 2 {
		t.Fatalf("usage = %+v", got)
	}
	busy := got.Items[0]
	if busy.Name != "busy" || busy.CPU != "1500m" || busy.Memory != "2048Mi" {
		t.Errorf("first item = %+v, want the busiest node first", busy)
	}
	if busy.CPUPercent == nil || *busy.CPUPercent != 75 || busy.MemoryPercent == nil || *busy.MemoryPercent != 50 {
		t.Errorf("percentages = %v / %v, want 75 / 50", busy.CPUPercent, busy.MemoryPercent)
	}
}

func TestTopPodsSumsContainersAndHonoursTheLimit(t *testing.T) {
	metrics := servingMetrics(nil, []metricsv1beta1.PodMetrics{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
			Containers: []metricsv1beta1.ContainerMetrics{
				{Name: "web", Usage: usage("200m", "100Mi")},
				{Name: "sidecar", Usage: usage("50m", "28Mi")},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "idle", Namespace: "apps"},
			Containers: []metricsv1beta1.ContainerMetrics{{Name: "idle", Usage: usage("1m", "10Mi")}},
		},
	})
	got, err := metricsBackend(nil, metrics).Top(context.Background(), ClusterOptions{}, TopQuery{Kind: "pods", Namespace: "apps", Limit: 1})
	if err != nil {
		t.Fatalf("Top: %v", err)
	}
	if got.Count != 1 || !got.Truncated {
		t.Fatalf("usage = %+v, want one item and truncated", got)
	}
	if got.Items[0].Name != "web" || got.Items[0].CPU != "250m" || got.Items[0].Memory != "128Mi" {
		t.Errorf("item = %+v, want web summed across both containers", got.Items[0])
	}
}

// No metrics-server is a normal cluster, not a broken tool.
func TestTopWithoutMetricsServerIsAnAnswerNotAnError(t *testing.T) {
	gr := schema.GroupResource{Group: "metrics.k8s.io", Resource: "nodes"}
	for name, fail := range map[string]error{
		"not installed": apierrors.NewNotFound(gr, ""),
		"not answering": apierrors.NewServiceUnavailable("the server is currently unable to handle the request"),
	} {
		t.Run(name, func(t *testing.T) {
			metrics := metricsfake.NewSimpleClientset()
			metrics.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, fail
			})
			got, err := metricsBackend(nil, metrics).Top(context.Background(), ClusterOptions{}, TopQuery{})
			if err != nil {
				t.Fatalf("Top returned an error for a cluster without metrics: %v", err)
			}
			if got.Available || got.Reason == "" {
				t.Errorf("usage = %+v, want available false with a reason", got)
			}
			assertSurvivesRedaction(t, "top reason", got.Reason)
		})
	}
}

func TestTopRejectsAnUnknownKind(t *testing.T) {
	_, err := metricsBackend(nil, metricsfake.NewSimpleClientset()).Top(context.Background(), ClusterOptions{}, TopQuery{Kind: "containers"})
	if err == nil || !strings.Contains(err.Error(), "nodes or pods") {
		t.Fatalf("err = %v", err)
	}
}
