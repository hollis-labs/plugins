package k8splugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func webDeployment() *appsv1.Deployment {
	three := int32(3)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "apps", CreationTimestamp: hoursAgo(5),
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"env":[{"name":"DB_PASSWORD","value":"` + sentinel + `"}]}`,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &three,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "web"},
					Annotations: map[string]string{"injected-config": sentinel},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "web",
						Image:   "web:1",
						Command: []string{"/bin/web", "--db-password=" + sentinel},
						Args:    []string{"--token", sentinel},
						Ports:   []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						Env:     []corev1.EnvVar{{Name: "DB_PASSWORD", Value: sentinel}},
						EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "web-secrets"},
						}}},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m")},
							Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
						},
						LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{Command: []string{"check", "--key=" + sentinel}},
						}},
						ReadinessProbe: &corev1.Probe{},
					}},
					Volumes: []corev1.Volume{
						{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "web-tls"}}},
						{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "web-data"}}},
						{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			ReadyReplicas: 2, UpdatedReplicas: 3, AvailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{
				{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable", LastTransitionTime: hoursAgo(1)},
			},
		},
	}
}

func TestDescribeWorkloadReportsTheUsefulSubset(t *testing.T) {
	objects := []runtime.Object{
		webDeployment(),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "apps", Labels: map[string]string{"app": "web"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "apps", Labels: map[string]string{"app": "other"}}},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "apps"},
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Name: "web"},
			Reason:         "ScalingReplicaSet", LastTimestamp: hoursAgo(1),
		},
		// Same name, different kind: must not be attributed to the deployment.
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "apps"},
			InvolvedObject: corev1.ObjectReference{Kind: "Service", Name: "web"},
			Reason:         "NotOurs",
		},
	}
	got, err := backendWith(objects...).DescribeWorkload(context.Background(), ClusterOptions{},
		WorkloadRef{Namespace: "apps", Kind: "deploy", Name: "web"})
	if err != nil {
		t.Fatalf("DescribeWorkload: %v", err)
	}

	if got.Kind != KindDeployment || got.Desired != 3 || got.Ready != 2 {
		t.Errorf("summary = %+v", got.Workload)
	}
	if got.Selector != "app=web" || got.Strategy != "RollingUpdate" {
		t.Errorf("selector/strategy = %q/%q", got.Selector, got.Strategy)
	}
	if len(got.Conditions) != 1 || got.Conditions[0].Reason != "MinimumReplicasUnavailable" {
		t.Errorf("conditions = %+v", got.Conditions)
	}
	if got.Pods.Count != 1 || got.Pods.Items[0].Name != "web-1" {
		t.Errorf("pods = %+v, want only the pod the selector matches", got.Pods)
	}
	if len(got.Events) != 1 || got.Events[0].Reason != "ScalingReplicaSet" {
		t.Errorf("events = %+v, want only the deployment's own", got.Events)
	}

	c := got.Containers[0]
	if strings.Join(c.Ports, ",") != "http:8080/TCP" {
		t.Errorf("ports = %v", c.Ports)
	}
	if c.Requests["cpu"] != "250m" || c.Limits["memory"] != "512Mi" {
		t.Errorf("resources = %v / %v", c.Requests, c.Limits)
	}
	if strings.Join(c.Probes, ",") != "liveness,readiness" {
		t.Errorf("probes = %v", c.Probes)
	}
	if strings.Join(got.Volumes, ",") != "secret/web-tls,pvc/web-data,emptyDir/cache" {
		t.Errorf("volumes = %v", got.Volumes)
	}
}

// describe reads more of the object than any list does — the template, the
// command, the probes — so it is the widest surface a credential could escape
// through. Every one of those places holds the sentinel here.
func TestDescribeWorkloadEmitsNoValueFromEnvArgsCommandProbesOrAnnotations(t *testing.T) {
	got, err := backendWith(webDeployment()).DescribeWorkload(context.Background(), ClusterOptions{},
		WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"})
	if err != nil {
		t.Fatalf("DescribeWorkload: %v", err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("describe_workload leaked a credential value:\n%s", data)
	}
	if !strings.Contains(string(data), "DB_PASSWORD") || !strings.Contains(string(data), "secret/web-secrets") {
		t.Errorf("names were lost along with the values:\n%s", data)
	}
}

func TestDescribeWorkloadRejectsAnUnknownKindBeforeCallingTheCluster(t *testing.T) {
	called := false
	backend := newClientGoBackendWith(func(ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
		called = true
		return fake.NewClientset(), &rest.Config{}, nil
	})
	_, err := backend.DescribeWorkload(context.Background(), ClusterOptions{}, WorkloadRef{Kind: "cronjob", Name: "x"})
	if err == nil || !strings.Contains(err.Error(), "expected one of") {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Error("the cluster was contacted for a request that was invalid on its face")
	}
}

func TestNormaliseKindAcceptsWhatOperatorsType(t *testing.T) {
	for in, want := range map[string]string{
		"deploy": KindDeployment, "Deployments": KindDeployment, " deployment ": KindDeployment,
		"sts": KindStatefulSet, "StatefulSet": KindStatefulSet,
		"ds": KindDaemonSet, "DAEMONSETS": KindDaemonSet,
	} {
		got, err := normaliseKind(in)
		if err != nil || got != want {
			t.Errorf("normaliseKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestServiceMappingCoversPortsAddressesAndSelector(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeLoadBalancer,
			ClusterIP: "10.0.0.10",
			Selector:  map[string]string{"app": "web"},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080), NodePort: 30080},
			},
		},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{
			Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.5"}},
		}},
	}
	list, err := backendWith(svc).Services(context.Background(), ClusterOptions{}, ScopedQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	got := list.Items[0]
	if got.Type != "LoadBalancer" || got.ClusterIP != "10.0.0.10" || got.Selector != "app=web" {
		t.Errorf("service = %+v", got)
	}
	if strings.Join(got.Ports, ",") != "http 80→8080/TCP:30080" {
		t.Errorf("ports = %v", got.Ports)
	}
	if strings.Join(got.ExternalAddresses, ",") != "203.0.113.5" {
		t.Errorf("addresses = %v", got.ExternalAddresses)
	}
}

func TestIngressMappingNamesTheTLSSecretAndRoutes(t *testing.T) {
	class := "nginx"
	prefix := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &class,
			TLS:              []networkingv1.IngressTLS{{Hosts: []string{"web.example.com"}, SecretName: "web-tls"}},
			Rules: []networkingv1.IngressRule{{
				Host: "web.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Path: "/api", PathType: &prefix,
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
							Name: "api", Port: networkingv1.ServiceBackendPort{Number: 8080},
						}},
					}},
				}},
			}},
		},
	}
	list, err := backendWith(ing).Ingresses(context.Background(), ClusterOptions{}, ScopedQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Ingresses: %v", err)
	}
	got := list.Items[0]
	if got.Class != "nginx" {
		t.Errorf("class = %q", got.Class)
	}
	if strings.Join(got.Routes, ",") != "web.example.com/api → api:8080" {
		t.Errorf("routes = %v", got.Routes)
	}
	if len(got.TLS) != 1 || got.TLS[0].Certificate != "secret/web-tls" {
		t.Errorf("tls = %+v", got.TLS)
	}
}

func TestAPIResourcesFiltersByGroupAndDropsSubresources(t *testing.T) {
	groups := []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{
			{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: []string{"get", "list"}, ShortNames: []string{"po"}},
			{Name: "pods/log", Kind: "Pod", Namespaced: true},
			{Name: "nodes", Kind: "Node", Verbs: []string{"get"}},
		}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
			{Name: "deployments", Kind: "Deployment", Namespaced: true},
			{Name: "deployments/scale", Kind: "Scale", Namespaced: true},
		}},
	}

	all := mapAPIResources(groups, []string{"metrics.k8s.io/v1beta1"}, APIResourceQuery{})
	var names []string
	for _, r := range all.Items {
		names = append(names, r.Group+"/"+r.Name)
	}
	if strings.Join(names, ",") != "/nodes,/pods,apps/deployments" {
		t.Errorf("resources = %v, want sorted, core first, no subresources", names)
	}
	if strings.Join(all.FailedGroups, ",") != "metrics.k8s.io/v1beta1" {
		t.Errorf("failed groups = %v; a partial answer must say what is missing", all.FailedGroups)
	}

	core := mapAPIResources(groups, nil, APIResourceQuery{Group: "core", Limit: 1})
	if core.Count != 1 || !core.Truncated || core.Items[0].Group != "" {
		t.Errorf("core filtered = %+v, want one core item and truncated", core)
	}

	apps := mapAPIResources(groups, nil, APIResourceQuery{Group: "apps"})
	if apps.Count != 1 || apps.Items[0].Kind != "Deployment" || apps.Truncated {
		t.Errorf("apps filtered = %+v", apps)
	}
}
