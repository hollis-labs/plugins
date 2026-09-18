package k8splugin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// These tests drive the real code in clientgo.go — the Backend methods and the
// mapping beneath them — using client-go's own fake clientset. The mapping is
// most of the risk in this package and none of it needs a cluster.
//
// What this does NOT verify, and what a real cluster is still needed for:
// the fake tracker ignores ListOptions.Limit and Continue, so the bounding
// added for real clusters is exercised as *logic* here, not against API server
// pagination semantics. Treat truncation as unverified until it has met an
// apiserver.
func backendWith(objects ...runtime.Object) Backend {
	clientset := fake.NewClientset(objects...)
	return newClientGoBackendWith(func(ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
		return clientset, &rest.Config{Host: "https://fake.example.com"}, nil
	})
}

func hoursAgo(h int) metav1.Time {
	return metav1.NewTime(time.Now().Add(-time.Duration(h) * time.Hour))
}

func TestNodeMappingReportsPressuresRolesAndReadiness(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "node-1",
			CreationTimestamp: hoursAgo(50),
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
				"node-role.kubernetes.io/worker":        "",
				"kubernetes.io/hostname":                "node-1",
			},
		},
		Spec: corev1.NodeSpec{Unschedulable: true},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				KubeletVersion:  "v1.37.0",
				OperatingSystem: "linux",
				Architecture:    "arm64",
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			},
		},
	}

	result, err := backendWith(node).Nodes(context.Background(), ClusterOptions{}, ListQuery{})
	if err != nil {
		t.Fatalf("Nodes: %v", err)
	}
	if result.Count != 1 {
		t.Fatalf("count = %d, want 1", result.Count)
	}
	got := result.Items[0]
	if !got.Ready {
		t.Error("Ready = false, want true")
	}
	if !got.Unschedulable {
		t.Error("Unschedulable = false; a cordoned node reporting schedulable is a misleading answer")
	}
	if strings.Join(got.Roles, ",") != "control-plane,worker" {
		t.Errorf("roles = %v, want both, sorted, and no other labels", got.Roles)
	}
	// Only conditions that are actually true, and not Ready duplicated.
	if strings.Join(got.Pressures, ",") != "MemoryPressure" {
		t.Errorf("pressures = %v, want only MemoryPressure", got.Pressures)
	}
	if got.Version != "v1.37.0" || got.Arch != "arm64" {
		t.Errorf("node info lost: %+v", got)
	}
	if got.Age == "" {
		t.Error("age is empty")
	}
}

func TestWorkloadMappingCoversAllThreeKinds(t *testing.T) {
	two := int32(2)
	objects := []runtime.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", CreationTimestamp: hoursAgo(3)},
			Spec: appsv1.DeploymentSpec{
				Replicas: &two,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "web", Image: "web:1"}},
				}},
			},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas: 1, UpdatedReplicas: 2, AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{
					{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"},
				},
			},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"},
			Spec: appsv1.StatefulSetSpec{
				Replicas: &two,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "db", Image: "postgres:16"}},
				}},
			},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "apps"},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "agent", Image: "agent:2"}},
			}}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, NumberReady: 3},
		},
	}

	result, err := backendWith(objects...).Workloads(context.Background(), ClusterOptions{},
		WorkloadQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Workloads: %v", err)
	}
	byKind := map[string]Workload{}
	for _, w := range result.Items {
		byKind[w.Kind] = w
	}
	if len(byKind) != 3 {
		t.Fatalf("got kinds %v, want Deployment, StatefulSet and DaemonSet", byKind)
	}

	deploy := byKind["Deployment"]
	if deploy.Desired != 2 || deploy.Ready != 1 {
		t.Errorf("deployment replicas = %d/%d, want 1/2", deploy.Ready, deploy.Desired)
	}
	if deploy.Condition != "Progressing: ReplicaSetUpdated" {
		t.Errorf("condition = %q", deploy.Condition)
	}
	if strings.Join(deploy.Images, ",") != "web:1" {
		t.Errorf("images = %v", deploy.Images)
	}
	// A DaemonSet has no Spec.Replicas; desired comes from status.
	if ds := byKind["DaemonSet"]; ds.Desired != 3 || ds.Ready != 3 {
		t.Errorf("daemonset = %d/%d, want 3/3", ds.Ready, ds.Desired)
	}
	if ss := byKind["StatefulSet"]; ss.Desired != 2 || ss.Ready != 2 {
		t.Errorf("statefulset = %d/%d, want 2/2", ss.Ready, ss.Desired)
	}
}

func TestWorkloadKindFilterSelectsOneAndRejectsATypo(t *testing.T) {
	objects := []runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"}},
	}
	backend := backendWith(objects...)

	// Singular and plural both work, because both get typed.
	for _, kind := range []string{"deployment", "deployments", "Deployment"} {
		result, err := backend.Workloads(context.Background(), ClusterOptions{},
			WorkloadQuery{Namespace: "apps", Kind: kind})
		if err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
		if result.Count != 1 || result.Items[0].Kind != "Deployment" {
			t.Errorf("kind %q returned %+v", kind, result.Items)
		}
	}

	// A typo must not silently read as "everything" — that is how an operator
	// concludes a filter worked when it did not.
	_, err := backend.Workloads(context.Background(), ClusterOptions{},
		WorkloadQuery{Namespace: "apps", Kind: "deployyment"})
	if err == nil {
		t.Fatal("want an error for an unknown kind")
	}
	if !strings.Contains(err.Error(), "deployyment") {
		t.Errorf("error does not echo the bad kind: %v", err)
	}
}

// An empty namespace means "the configured default", never "the whole cluster".
// Asking for every namespace has to be deliberate.
func TestAllNamespacesIsOptInAndNamespaceScopingHolds(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "apps"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "other"}},
	}
	backend := backendWith(objects...)

	scoped, err := backend.Pods(context.Background(), ClusterOptions{}, PodQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Pods: %v", err)
	}
	if scoped.Count != 1 || scoped.Items[0].Name != "a" {
		t.Errorf("namespace scoping failed: %+v", scoped.Items)
	}

	all, err := backend.Pods(context.Background(), ClusterOptions{},
		PodQuery{Namespace: "apps", AllNamespaces: true})
	if err != nil {
		t.Fatalf("Pods all: %v", err)
	}
	if all.Count != 2 {
		t.Errorf("all-namespaces returned %d, want 2 — the namespace should be ignored", all.Count)
	}
}

func TestPodLabelSelectorIsApplied(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps", Labels: map[string]string{"app": "web"}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps", Labels: map[string]string{"app": "db"}}},
	}
	result, err := backendWith(objects...).Pods(context.Background(), ClusterOptions{},
		PodQuery{Namespace: "apps", Selector: "app=web"})
	if err != nil {
		t.Fatalf("Pods: %v", err)
	}
	if result.Count != 1 || result.Items[0].Name != "web" {
		t.Errorf("selector not applied: %+v", result.Items)
	}
}

func TestEventsAreSortedNewestLast(t *testing.T) {
	objects := []runtime.Object{
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e-old", Namespace: "apps"},
			Type:           "Warning",
			Reason:         "BackOff",
			Message:        "Back-off restarting failed container",
			Count:          7,
			LastTimestamp:  hoursAgo(5),
			FirstTimestamp: hoursAgo(9),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web-1"},
		},
		&corev1.Event{
			ObjectMeta:    metav1.ObjectMeta{Name: "e-new", Namespace: "apps"},
			Type:          "Normal",
			Reason:        "Scheduled",
			LastTimestamp: hoursAgo(1),
		},
	}
	result, err := backendWith(objects...).Events(context.Background(), ClusterOptions{},
		EventQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("count = %d", result.Count)
	}
	if result.Items[0].Reason != "BackOff" || result.Items[1].Reason != "Scheduled" {
		t.Errorf("events not sorted newest last: %+v", result.Items)
	}
	first := result.Items[0]
	if first.Object != "Pod/web-1" || first.Count != 7 {
		t.Errorf("event lost its subject or count: %+v", first)
	}
	if first.FirstSeen == "" || first.LastSeen == "" {
		t.Errorf("event timestamps dropped: %+v", first)
	}
}

// Events written through the newer events.k8s.io path carry EventTime and leave
// LastTimestamp empty. Without the fallback they sort as the oldest thing in
// the list, which puts the most recent event at the top of a list documented as
// newest-last.
func TestEventWithOnlyEventTimeStillGetsATimestamp(t *testing.T) {
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "apps"},
		Reason:     "Started",
		EventTime:  metav1.NewMicroTime(time.Now().Add(-2 * time.Hour)),
	}
	result, err := backendWith(event).Events(context.Background(), ClusterOptions{}, EventQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if result.Items[0].LastSeen == "" {
		t.Error("LastSeen is empty for an events.k8s.io-style event")
	}
}

func TestNamespaceListingMapsStatusAndAge(t *testing.T) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "apps", CreationTimestamp: hoursAgo(72)},
		Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
	}
	result, err := backendWith(ns).Namespaces(context.Background(), ClusterOptions{}, ListQuery{})
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	got := result.Items[0]
	if got.Status != "Active" || got.Age == "" || got.Created == "" {
		t.Errorf("namespace = %+v", got)
	}
}

// An empty list must marshal as [] rather than null: a tool result of "null"
// reads to an agent as an error or a missing field, not as "nothing here".
func TestEmptyListMarshalsAsAnEmptyArray(t *testing.T) {
	result, err := backendWith().Pods(context.Background(), ClusterOptions{}, PodQuery{Namespace: "apps"})
	if err != nil {
		t.Fatalf("Pods: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"items":[]`) {
		t.Errorf("empty list marshalled as %s, want an empty array", encoded)
	}
	if result.Truncated {
		t.Error("an empty result claims to be truncated")
	}
}

func TestHealthReportsTheServerAndVersionItReached(t *testing.T) {
	health, err := backendWith().Health(context.Background(), ClusterOptions{Context: "fake-ctx"})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Reachable {
		t.Fatalf("Reachable = false: %s", health.Message)
	}
	if health.Context != "fake-ctx" || health.Server != "https://fake.example.com" {
		t.Errorf("health = %+v, want the context and server echoed back", health)
	}
}

// A connection that cannot be built is reported as unreachable with a reason,
// never as an error — Health is the operation an operator runs *because*
// something is wrong, so it has to answer.
func TestHealthReportsAResolutionFailureRatherThanErroring(t *testing.T) {
	backend := newClientGoBackendWith(func(ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
		return nil, nil, apierrors.NewUnauthorized("no credential")
	})
	health, err := backend.Health(context.Background(), ClusterOptions{})
	if err != nil {
		t.Fatalf("Health returned an error instead of an unreachable result: %v", err)
	}
	if health.Reachable || health.Message == "" {
		t.Errorf("health = %+v, want unreachable with a reason", health)
	}
}

func TestDescribeErrorNamesTheCauseForEachAPIFailure(t *testing.T) {
	server := "https://k8s.corp.example.com:6443"
	gvr := schema.GroupResource{Resource: "pods"}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"unauthorized", apierrors.NewUnauthorized("expired"), "missing or expired"},
		{"forbidden", apierrors.NewForbidden(gvr, "web-1", nil), "not permitted"},
		{"timeout", apierrors.NewTimeoutError("too slow", 1), "VPN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := describeError(tc.err, server)
			if !strings.Contains(got, tc.want) {
				t.Errorf("describeError = %q, want it to mention %q", got, tc.want)
			}
			assertSurvivesRedaction(t, "describeError/"+tc.name, got)
		})
	}

	// A forbidden read is an RBAC grant to request, not a bug to work around.
	// The message has to say so, because this connector is read-only and the
	// obvious next move otherwise is to try a write path.
	forbidden := describeError(apierrors.NewForbidden(gvr, "web-1", nil), server)
	if !strings.Contains(forbidden, "read-only") {
		t.Errorf("forbidden message does not set the expectation: %q", forbidden)
	}
}

func TestBoundLimitClampsAndDefaults(t *testing.T) {
	cases := []struct{ requested, fallback, max, want int }{
		{0, 100, 1000, 100},
		{-5, 100, 1000, 100},
		{50, 100, 1000, 50},
		{5000, 100, 1000, 1000},
	}
	for _, tc := range cases {
		if got := boundLimit(tc.requested, tc.fallback, tc.max); got != tc.want {
			t.Errorf("boundLimit(%d, %d, %d) = %d, want %d", tc.requested, tc.fallback, tc.max, got, tc.want)
		}
	}
}

func TestWorkloadKindFilterIsCaseInsensitiveIncludingPlurals(t *testing.T) {
	backend := backendWith(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
	})
	// Operators paste from all sorts of places; an upper-case plural losing its
	// S to the trim and then failing the match is a confusing way to be told
	// "unknown kind".
	for _, kind := range []string{"DEPLOYMENTS", "Deployments", " deployment ", "DaemonSets"} {
		if _, err := backend.Workloads(context.Background(), ClusterOptions{},
			WorkloadQuery{Namespace: "apps", Kind: kind}); err != nil {
			t.Errorf("kind %q rejected: %v", kind, err)
		}
	}
}

func TestContainerStateCoversWaitingAndTerminated(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "app"}, {Name: "sidecar"}, {Name: "init-ish"},
		}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "app", RestartCount: 4,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
						Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container",
					}},
				},
				{
					Name: "sidecar",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						Reason: "Error", Message: "exit status 1",
					}},
				},
				{Name: "init-ish"}, // no state set at all
			},
		},
	}

	dto := mapPod(pod)
	byName := map[string]Container{}
	for _, c := range dto.Containers {
		byName[c.Name] = c
	}
	// CrashLoopBackOff is the single most useful thing a pod listing can say.
	if got := byName["app"]; got.State != "waiting" || got.Reason != "CrashLoopBackOff" {
		t.Errorf("app container = %+v, want waiting/CrashLoopBackOff", got)
	}
	if got := byName["sidecar"]; got.State != "terminated" || got.Reason != "Error" {
		t.Errorf("sidecar container = %+v", got)
	}
	if got := byName["init-ish"]; got.State != "unknown" {
		t.Errorf("stateless container = %+v, want unknown rather than empty", got)
	}
	if dto.Ready != "0/3" || dto.Restarts != 4 {
		t.Errorf("pod = %s ready, %d restarts; want 0/3 and 4", dto.Ready, dto.Restarts)
	}
}

func TestLogsReturnsABoundedSnapshot(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	snapshot, err := backendWith(pod).Logs(context.Background(), ClusterOptions{}, LogQuery{
		Namespace: "apps", Pod: "web", Container: "app", Tail: 10,
	})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if snapshot.Pod != "web" || snapshot.Namespace != "apps" || snapshot.Container != "app" {
		t.Errorf("snapshot lost its subject: %+v", snapshot)
	}
	// Lines must never be nil: a tool result of "lines": null reads as an error.
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"lines":null`) {
		t.Errorf("log snapshot marshalled nil lines: %s", encoded)
	}
}

func TestDescribeErrorNamesTheNetworkCauseAndPointsAtTheVPN(t *testing.T) {
	server := "https://k8s.corp.example.com:6443"

	dnsFailure := &url.Error{
		Op:  "Get",
		URL: server,
		Err: &net.DNSError{Err: "no such host", Name: "k8s.corp.example.com", IsNotFound: true},
	}
	got := describeError(dnsFailure, server)
	if !strings.Contains(got, "cannot resolve") || !strings.Contains(got, "VPN") {
		t.Errorf("DNS failure = %q, want it to name resolution and the VPN", got)
	}
	assertSurvivesRedaction(t, "describeError/dns", got)

	refused := &url.Error{
		Op:  "Get",
		URL: server,
		Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")},
	}
	got = describeError(refused, server)
	if !strings.Contains(got, "cannot reach") || !strings.Contains(got, "VPN") {
		t.Errorf("refused dial = %q, want it to name reachability and the VPN", got)
	}
	assertSurvivesRedaction(t, "describeError/refused", got)

	if describeError(nil, server) != "" {
		t.Error("describeError(nil) should be empty")
	}
}

// RestConfig rewrites a resolved exec helper to an absolute path before
// client-go execs it, because the daemon's PATH will not find it. The
// operator's kubeconfig on disk must not be touched in the process.
func TestRestConfigRewritesTheExecHelperWithoutModifyingTheFile(t *testing.T) {
	binDir := t.TempDir()
	helper := filepath.Join(binDir, "cerberus-test-rewrite-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := `apiVersion: v1
kind: Config
current-context: c1
clusters:
- name: c
  cluster: {server: https://api.example.com}
contexts:
- name: c1
  context: {cluster: c, user: u}
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: cerberus-test-rewrite-helper
      interactiveMode: Never
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}

	cfg, mode, err := RestConfig(ClusterOptions{Kubeconfig: path, CredentialPath: binDir})
	if err != nil {
		t.Fatalf("RestConfig: %v", err)
	}
	if mode != AuthModeExec {
		t.Errorf("mode = %q, want exec", mode)
	}
	if cfg.ExecProvider == nil {
		t.Fatal("no exec provider on the resulting config")
	}
	if cfg.ExecProvider.Command != helper {
		t.Errorf("exec command = %q, want the absolute path %q", cfg.ExecProvider.Command, helper)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read kubeconfig: %v", err)
	}
	if string(before) != string(after) {
		t.Error("RestConfig modified the operator's kubeconfig on disk")
	}
}

// A missing helper must fail before any network call, with the credential_missing
// code, rather than surfacing later as an authentication failure.
func TestRestConfigFailsFastWhenTheExecHelperIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	body := `apiVersion: v1
kind: Config
current-context: c1
clusters:
- name: c
  cluster: {server: https://api.example.com}
contexts:
- name: c1
  context: {cluster: c, user: u}
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: cerberus-test-absent-helper
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	_, mode, err := RestConfig(ClusterOptions{Kubeconfig: path})
	if err == nil {
		t.Fatal("want a credential_missing error")
	}
	if mode != AuthModeExec {
		t.Errorf("mode = %q, want the classification even on failure", mode)
	}
	if !strings.HasPrefix(err.Error(), "credential_missing:") {
		t.Errorf("error = %q, want the credential_missing code", err.Error())
	}
}
