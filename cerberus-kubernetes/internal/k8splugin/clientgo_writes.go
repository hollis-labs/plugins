package k8splugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// FieldManager is the name every write is recorded under in the object's
// managedFields. It is what makes a change attributable to Cerberus after the
// fact, rather than indistinguishable from an operator's own kubectl.
const FieldManager = "cerberus-kubernetes-plugin"

// RestartAnnotation is the pod-template annotation `kubectl rollout restart`
// sets. Using the same key means a restart from here and one from kubectl are
// the same operation to the controller and to anyone reading the object.
const RestartAnnotation = "kubectl.kubernetes.io/restartedAt"

// Every write below follows the same shape:
//
//  1. Read the current state, so the result can say what changed and a no-op
//     can be recognised and skipped rather than sent.
//  2. Send the change with dryRun=All when a dry run was asked for. The API
//     server runs admission, validation and defaulting and discards the result,
//     so a dry run answers "would the cluster accept this", including RBAC.
//  3. Report a Change built from the fields this operation touches — never the
//     object the server returned.

func dryRunOption(dryRun bool) []string {
	if dryRun {
		return []string{metav1.DryRunAll}
	}
	return nil
}

func workloadTarget(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func (b *clientGoBackend) Scale(ctx context.Context, opts ClusterOptions, req ScaleRequest) (Change, error) {
	kind, err := normaliseKind(req.Kind)
	if err != nil {
		return Change{}, err
	}
	if kind == KindDaemonSet {
		return Change{}, fmt.Errorf("a DaemonSet runs one pod per eligible node and has no replica count to scale; change its node selector or tolerations instead")
	}
	if req.Replicas < 0 {
		return Change{}, fmt.Errorf("replicas must be zero or more, got %d", req.Replicas)
	}
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Change{}, err
	}

	change := Change{Operation: "scale_workload", Target: workloadTarget(kind, req.Namespace, req.Name), DryRun: req.DryRun}

	// The scale subresource rather than a patch of spec.replicas: it is what
	// kubectl scale uses, and it is what an RBAC role scoped to scaling grants
	// (deployments/scale), so a role that may scale but not edit still works.
	get, update := scaleClient(cs, kind, req.Namespace)
	current, err := get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("read scale of %s: %s", change.Target, describeError(err, cfg.Host)))
	}
	before := current.Spec.Replicas
	if before == req.Replicas {
		change.Message = fmt.Sprintf("already at %d replicas; nothing sent", before)
		return change, nil
	}
	if req.Replicas == 0 {
		change.Warnings = append(change.Warnings, "scaling to zero stops every pod of this workload; anything routed to it will fail until it is scaled back up")
	}

	current.Spec.Replicas = req.Replicas
	result, err := update(ctx, req.Name, current, metav1.UpdateOptions{DryRun: dryRunOption(req.DryRun), FieldManager: FieldManager})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("scale %s: %s", change.Target, describeError(err, cfg.Host)))
	}
	change.Applied = !req.DryRun
	change.Changes = []FieldChange{{
		Field:  "spec.replicas",
		Before: strconv.Itoa(int(before)),
		After:  strconv.Itoa(int(result.Spec.Replicas)),
	}}
	return change, nil
}

type (
	getScaleFunc    func(context.Context, string, metav1.GetOptions) (*autoscalingv1.Scale, error)
	updateScaleFunc func(context.Context, string, *autoscalingv1.Scale, metav1.UpdateOptions) (*autoscalingv1.Scale, error)
)

func scaleClient(cs kubernetes.Interface, kind, namespace string) (getScaleFunc, updateScaleFunc) {
	if kind == KindStatefulSet {
		c := cs.AppsV1().StatefulSets(namespace)
		return c.GetScale, c.UpdateScale
	}
	c := cs.AppsV1().Deployments(namespace)
	return c.GetScale, c.UpdateScale
}

func (b *clientGoBackend) Restart(ctx context.Context, opts ClusterOptions, req RestartRequest) (Change, error) {
	kind, err := normaliseKind(req.Kind)
	if err != nil {
		return Change{}, err
	}
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Change{}, err
	}
	change := Change{Operation: "restart_workload", Target: workloadTarget(kind, req.Namespace, req.Name), DryRun: req.DryRun}

	// Read the one annotation this operation writes, for the before value.
	// Only that key: the template's other annotations are not ours to report.
	var previous string
	switch kind {
	case KindDeployment:
		d, err := cs.AppsV1().Deployments(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
		if err != nil {
			return Change{}, coded(err, fmt.Errorf("read %s: %s", change.Target, describeError(err, cfg.Host)))
		}
		previous = d.Spec.Template.Annotations[RestartAnnotation]
		if d.Spec.Paused {
			change.Warnings = append(change.Warnings, "this deployment is paused; the restart is recorded but no pods will be replaced until it is resumed")
		}
	case KindStatefulSet:
		s, err := cs.AppsV1().StatefulSets(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
		if err != nil {
			return Change{}, coded(err, fmt.Errorf("read %s: %s", change.Target, describeError(err, cfg.Host)))
		}
		previous = s.Spec.Template.Annotations[RestartAnnotation]
		if s.Spec.UpdateStrategy.Type == "OnDelete" {
			change.Warnings = append(change.Warnings, "this statefulset uses the OnDelete update strategy; the restart is recorded but pods are only replaced when each one is deleted")
		}
	case KindDaemonSet:
		d, err := cs.AppsV1().DaemonSets(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
		if err != nil {
			return Change{}, coded(err, fmt.Errorf("read %s: %s", change.Target, describeError(err, cfg.Host)))
		}
		previous = d.Spec.Template.Annotations[RestartAnnotation]
		if d.Spec.UpdateStrategy.Type == "OnDelete" {
			change.Warnings = append(change.Warnings, "this daemonset uses the OnDelete update strategy; the restart is recorded but pods are only replaced when each one is deleted")
		}
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
			"annotations": map[string]string{RestartAnnotation: stamp},
		}}},
	})
	if err != nil {
		return Change{}, fmt.Errorf("build restart patch: %w", err)
	}
	patchOpts := metav1.PatchOptions{DryRun: dryRunOption(req.DryRun), FieldManager: FieldManager}
	switch kind {
	case KindDeployment:
		_, err = cs.AppsV1().Deployments(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, patchOpts)
	case KindStatefulSet:
		_, err = cs.AppsV1().StatefulSets(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, patchOpts)
	case KindDaemonSet:
		_, err = cs.AppsV1().DaemonSets(req.Namespace).Patch(ctx, req.Name, types.StrategicMergePatchType, patch, patchOpts)
	}
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("restart %s: %s", change.Target, describeError(err, cfg.Host)))
	}
	change.Applied = !req.DryRun
	change.Changes = []FieldChange{{
		Field:  "spec.template.metadata.annotations[" + RestartAnnotation + "]",
		Before: previous,
		After:  stamp,
	}}
	change.Message = "pods are replaced at the pace the rollout strategy allows; follow progress with describe_workload"
	return change, nil
}

func (b *clientGoBackend) SetSchedulable(ctx context.Context, opts ClusterOptions, req SchedulableRequest) (Change, error) {
	operation := "cordon_node"
	if req.Schedulable {
		operation = "uncordon_node"
	}
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Change{}, err
	}
	change := Change{Operation: operation, Target: "Node/" + req.Node, DryRun: req.DryRun}

	node, err := cs.CoreV1().Nodes().Get(ctx, req.Node, metav1.GetOptions{})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("read %s: %s", change.Target, describeError(err, cfg.Host)))
	}
	wantUnschedulable := !req.Schedulable
	if node.Spec.Unschedulable == wantUnschedulable {
		if wantUnschedulable {
			change.Message = "already cordoned; nothing sent"
		} else {
			change.Message = "already schedulable; nothing sent"
		}
		return change, nil
	}

	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"unschedulable": wantUnschedulable}})
	if err != nil {
		return Change{}, fmt.Errorf("build node patch: %w", err)
	}
	result, err := cs.CoreV1().Nodes().Patch(ctx, req.Node, types.MergePatchType, patch,
		metav1.PatchOptions{DryRun: dryRunOption(req.DryRun), FieldManager: FieldManager})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("%s %s: %s", operation, change.Target, describeError(err, cfg.Host)))
	}
	change.Applied = !req.DryRun
	change.Changes = []FieldChange{{
		Field:  "spec.unschedulable",
		Before: strconv.FormatBool(node.Spec.Unschedulable),
		After:  strconv.FormatBool(result.Spec.Unschedulable),
	}}
	if wantUnschedulable {
		// Cordon is the half of drain that stops new pods arriving; it does not
		// move the ones already there. Saying so prevents the natural misreading.
		change.Message = "new pods will not be scheduled here; pods already on the node keep running — this is not a drain"
	}
	return change, nil
}

func (b *clientGoBackend) DeletePod(ctx context.Context, opts ClusterOptions, req DeletePodRequest) (Change, error) {
	if req.GracePeriod != nil && *req.GracePeriod < 0 {
		return Change{}, fmt.Errorf("grace_period_seconds must be zero or more, got %d", *req.GracePeriod)
	}
	cs, cfg, err := b.clientset(opts)
	if err != nil {
		return Change{}, err
	}
	change := Change{Operation: "delete_pod", Target: workloadTarget("Pod", req.Namespace, req.Name), DryRun: req.DryRun}

	pod, err := cs.CoreV1().Pods(req.Namespace).Get(ctx, req.Name, metav1.GetOptions{})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("read %s: %s", change.Target, describeError(err, cfg.Host)))
	}

	// Whether the pod comes back is the thing an operator deleting one is
	// actually asking, and it depends entirely on who owns it.
	controller := metav1.GetControllerOf(pod)
	switch {
	case controller == nil:
		change.Warnings = append(change.Warnings, "no controller owns this pod, so nothing will recreate it; deleting it removes it for good")
	case controller.Kind == "Job":
		change.Warnings = append(change.Warnings, "this pod belongs to Job/"+controller.Name+"; whether it is retried depends on the job's backoffLimit, and a completed job does not rerun")
	default:
		change.Message = controller.Kind + "/" + controller.Name + " owns this pod and will replace it"
	}
	if pod.DeletionTimestamp != nil {
		change.Warnings = append(change.Warnings, "this pod is already terminating; deleting it again only matters with a shorter grace period")
	}
	if req.GracePeriod != nil && *req.GracePeriod == 0 {
		change.Warnings = append(change.Warnings, "a zero grace period skips graceful shutdown; the container gets no chance to finish in-flight work")
	}

	err = cs.CoreV1().Pods(req.Namespace).Delete(ctx, req.Name, metav1.DeleteOptions{
		DryRun:             dryRunOption(req.DryRun),
		GracePeriodSeconds: req.GracePeriod,
	})
	if err != nil {
		return Change{}, coded(err, fmt.Errorf("delete %s: %s", change.Target, describeError(err, cfg.Host)))
	}
	change.Applied = !req.DryRun
	change.Changes = []FieldChange{{Field: "pod", Before: string(pod.Status.Phase), After: "deleted"}}
	return change, nil
}
