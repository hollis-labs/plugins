package k8splugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// These drive the write paths against client-go's fake clientset. The fake
// ignores dryRun — it applies every change — so what is asserted here is that
// the request *asked* for dryRun=All and attributed itself, read off the
// recorded actions. Whether the API server honours it is verified against a
// real cluster; see the plan's verification section.

func writeBackend(objects ...runtime.Object) (Backend, *fake.Clientset) {
	clientset := fake.NewClientset(objects...)
	backend := newClientGoBackendWith(func(ClusterOptions) (kubernetes.Interface, *rest.Config, error) {
		return clientset, &rest.Config{Host: "https://fake.example.com"}, nil
	})
	return backend, clientset
}

// withScaleSubresource teaches the fake the scale subresource, which its
// object tracker does not model: get returns the deployment's replicas, update
// records the request and echoes it back.
func withScaleSubresource(clientset *fake.Clientset, replicas int32) *autoscalingv1.Scale {
	var updated autoscalingv1.Scale
	clientset.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		return true, &autoscalingv1.Scale{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"},
			Spec:       autoscalingv1.ScaleSpec{Replicas: replicas},
		}, nil
	})
	clientset.PrependReactor("update", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		scale := action.(k8stesting.UpdateAction).GetObject().(*autoscalingv1.Scale)
		updated = *scale
		return true, scale, nil
	})
	return &updated
}

func updateOptions(t *testing.T, clientset *fake.Clientset, verb, subresource string) metav1.UpdateOptions {
	t.Helper()
	for _, action := range clientset.Actions() {
		if action.GetVerb() == verb && action.GetSubresource() == subresource {
			if update, ok := action.(k8stesting.UpdateActionImpl); ok {
				return update.UpdateOptions
			}
		}
	}
	t.Fatalf("no %s %s action recorded in %v", verb, subresource, clientset.Actions())
	return metav1.UpdateOptions{}
}

func patchOptions(t *testing.T, clientset *fake.Clientset) (metav1.PatchOptions, []byte) {
	t.Helper()
	for _, action := range clientset.Actions() {
		if patch, ok := action.(k8stesting.PatchActionImpl); ok {
			return patch.PatchOptions, patch.Patch
		}
	}
	t.Fatalf("no patch action recorded in %v", clientset.Actions())
	return metav1.PatchOptions{}, nil
}

func TestScaleUsesTheScaleSubresourceAndReportsBeforeAndAfter(t *testing.T) {
	backend, clientset := writeBackend()
	updated := withScaleSubresource(clientset, 2)

	change, err := backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{
		WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}, Replicas: 5,
	})
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if updated.Spec.Replicas != 5 {
		t.Errorf("sent replicas = %d, want 5", updated.Spec.Replicas)
	}
	if !change.Applied || change.DryRun {
		t.Errorf("applied/dry_run = %v/%v", change.Applied, change.DryRun)
	}
	if len(change.Changes) != 1 || change.Changes[0].Before != "2" || change.Changes[0].After != "5" {
		t.Errorf("changes = %+v", change.Changes)
	}
	opts := updateOptions(t, clientset, "update", "scale")
	if len(opts.DryRun) != 0 {
		t.Errorf("a real write asked for dryRun %v", opts.DryRun)
	}
	if opts.FieldManager != FieldManager {
		t.Errorf("field manager = %q; the write is not attributable to Cerberus", opts.FieldManager)
	}
}

func TestScaleDryRunAsksTheAPIServerForDryRunAll(t *testing.T) {
	backend, clientset := writeBackend()
	withScaleSubresource(clientset, 2)

	change, err := backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{
		WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}, Replicas: 0, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if change.Applied || !change.DryRun {
		t.Errorf("a dry run reported applied=%v dry_run=%v", change.Applied, change.DryRun)
	}
	if opts := updateOptions(t, clientset, "update", "scale"); strings.Join(opts.DryRun, ",") != metav1.DryRunAll {
		t.Errorf("dryRun = %v, want [All]", opts.DryRun)
	}
	if len(change.Warnings) == 0 || !strings.Contains(change.Warnings[0], "zero") {
		t.Errorf("scaling to zero carried no warning: %+v", change.Warnings)
	}
}

func TestScaleToTheCurrentCountSendsNothing(t *testing.T) {
	backend, clientset := writeBackend()
	withScaleSubresource(clientset, 3)

	change, err := backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{
		WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}, Replicas: 3,
	})
	if err != nil {
		t.Fatalf("Scale: %v", err)
	}
	if change.Applied {
		t.Error("a no-op reported applied")
	}
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "update" {
			t.Errorf("a no-op sent an update: %v", action)
		}
	}
}

func TestScaleRefusesADaemonSetAndANegativeCount(t *testing.T) {
	backend, _ := writeBackend()
	if _, err := backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{
		WorkloadRef: WorkloadRef{Kind: "daemonset", Name: "agent"}, Replicas: 2,
	}); err == nil || !strings.Contains(err.Error(), "no replica count") {
		t.Errorf("daemonset err = %v", err)
	}
	if _, err := backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{
		WorkloadRef: WorkloadRef{Kind: "deployment", Name: "web"}, Replicas: -1,
	}); err == nil {
		t.Error("a negative replica count was accepted")
	}
}

func TestRestartStampsThePodTemplateAsKubectlDoes(t *testing.T) {
	d := webDeployment()
	d.Spec.Template.Annotations[RestartAnnotation] = "2026-01-01T00:00:00Z"
	backend, clientset := writeBackend(d)

	change, err := backend.Restart(context.Background(), ClusterOptions{}, RestartRequest{
		WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	opts, patch := patchOptions(t, clientset)
	if strings.Join(opts.DryRun, ",") != metav1.DryRunAll || opts.FieldManager != FieldManager {
		t.Errorf("patch options = %+v", opts)
	}
	if !strings.Contains(string(patch), RestartAnnotation) {
		t.Errorf("patch = %s, want the rollout-restart annotation", patch)
	}
	if change.Changes[0].Before != "2026-01-01T00:00:00Z" || change.Changes[0].After == "" {
		t.Errorf("changes = %+v", change.Changes)
	}

	// The result reports the one annotation it wrote and nothing else from the
	// template — webDeployment's template carries the sentinel in another one.
	data, _ := json.Marshal(change)
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("restart result leaked another template annotation:\n%s", data)
	}
}

func TestRestartWarnsWhenNothingWillActuallyRoll(t *testing.T) {
	d := webDeployment()
	d.Spec.Paused = true
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "apps"},
		Spec:       appsv1.StatefulSetSpec{UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}},
	}
	backend, _ := writeBackend(d, sts)

	for _, ref := range []WorkloadRef{
		{Namespace: "apps", Kind: "deployment", Name: "web"},
		{Namespace: "apps", Kind: "statefulset", Name: "db"},
	} {
		change, err := backend.Restart(context.Background(), ClusterOptions{}, RestartRequest{WorkloadRef: ref})
		if err != nil {
			t.Fatalf("Restart %s: %v", ref.Kind, err)
		}
		if len(change.Warnings) == 0 {
			t.Errorf("%s: a restart that replaces no pods carried no warning", ref.Kind)
		}
	}
}

func TestCordonPatchesSchedulabilityAndSaysItIsNotADrain(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	backend, clientset := writeBackend(node)

	change, err := backend.SetSchedulable(context.Background(), ClusterOptions{}, SchedulableRequest{Node: "worker-1"})
	if err != nil {
		t.Fatalf("cordon: %v", err)
	}
	if change.Operation != "cordon_node" || !change.Applied {
		t.Errorf("change = %+v", change)
	}
	if change.Changes[0].Before != "false" || change.Changes[0].After != "true" {
		t.Errorf("changes = %+v", change.Changes)
	}
	if !strings.Contains(change.Message, "not a drain") {
		t.Errorf("message = %q", change.Message)
	}
	_, patch := patchOptions(t, clientset)
	if string(patch) != `{"spec":{"unschedulable":true}}` {
		t.Errorf("patch = %s", patch)
	}

	// Cordoning again is a no-op and sends nothing further.
	before := len(clientset.Actions())
	again, err := backend.SetSchedulable(context.Background(), ClusterOptions{}, SchedulableRequest{Node: "worker-1"})
	if err != nil {
		t.Fatalf("cordon again: %v", err)
	}
	if again.Applied {
		t.Error("a no-op cordon reported applied")
	}
	for _, action := range clientset.Actions()[before:] {
		if action.GetVerb() == "patch" {
			t.Error("a no-op cordon sent a patch")
		}
	}

	un, err := backend.SetSchedulable(context.Background(), ClusterOptions{}, SchedulableRequest{Node: "worker-1", Schedulable: true})
	if err != nil {
		t.Fatalf("uncordon: %v", err)
	}
	if un.Operation != "uncordon_node" || un.Changes[0].After != "false" {
		t.Errorf("uncordon = %+v", un)
	}
}

func TestDeletePodSaysWhetherThePodComesBack(t *testing.T) {
	yes := true
	owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-1", Namespace: "apps",
		OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-5d4f", Controller: &yes}},
	}}
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "apps"}}
	backend, clientset := writeBackend(owned, bare)

	change, err := backend.DeletePod(context.Background(), ClusterOptions{}, DeletePodRequest{Namespace: "apps", Name: "web-1", DryRun: true})
	if err != nil {
		t.Fatalf("DeletePod owned: %v", err)
	}
	if !strings.Contains(change.Message, "ReplicaSet/web-5d4f") || len(change.Warnings) != 0 {
		t.Errorf("owned pod: message %q warnings %v", change.Message, change.Warnings)
	}
	for _, action := range clientset.Actions() {
		if del, ok := action.(k8stesting.DeleteActionImpl); ok {
			if strings.Join(del.DeleteOptions.DryRun, ",") != metav1.DryRunAll {
				t.Errorf("delete dryRun = %v, want [All]", del.DeleteOptions.DryRun)
			}
		}
	}

	zero := int64(0)
	change, err = backend.DeletePod(context.Background(), ClusterOptions{}, DeletePodRequest{Namespace: "apps", Name: "debug", GracePeriod: &zero})
	if err != nil {
		t.Fatalf("DeletePod bare: %v", err)
	}
	if len(change.Warnings) != 2 {
		t.Errorf("bare pod with zero grace: warnings = %v, want the permanence and the grace period both named", change.Warnings)
	}
	if !change.Applied {
		t.Error("real delete not reported applied")
	}
}

// Every Change message and warning is operator-facing text that crosses the
// host's error and output paths.
func TestWriteMessagesSurviveRedaction(t *testing.T) {
	d := webDeployment()
	d.Spec.Paused = true
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "apps"}}
	backend, clientset := writeBackend(d, node, pod)
	withScaleSubresource(clientset, 2)
	zero := int64(0)

	var changes []Change
	for _, run := range []func() (Change, error){
		func() (Change, error) {
			return backend.Scale(context.Background(), ClusterOptions{}, ScaleRequest{WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}})
		},
		func() (Change, error) {
			return backend.Restart(context.Background(), ClusterOptions{}, RestartRequest{WorkloadRef: WorkloadRef{Namespace: "apps", Kind: "deployment", Name: "web"}})
		},
		func() (Change, error) {
			return backend.SetSchedulable(context.Background(), ClusterOptions{}, SchedulableRequest{Node: "worker-1"})
		},
		func() (Change, error) {
			return backend.DeletePod(context.Background(), ClusterOptions{}, DeletePodRequest{Namespace: "apps", Name: "debug", GracePeriod: &zero})
		},
	} {
		change, err := run()
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		changes = append(changes, change)
	}
	for _, change := range changes {
		assertSurvivesRedaction(t, change.Operation+" message", change.Message)
		for _, warning := range change.Warnings {
			assertSurvivesRedaction(t, change.Operation+" warning", warning)
		}
	}
}
