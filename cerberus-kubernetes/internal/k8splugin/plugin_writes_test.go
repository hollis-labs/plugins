package k8splugin

import (
	"context"
	"strings"
	"testing"

	cerbplugin "github.com/hollis-labs/cerberus/pkg/plugin"
	"github.com/hollis-labs/plugin-sdk/subprocess"
)

func callErr(p *Plugin, operation string, args map[string]any) error {
	_, err := p.MCPCallTool(context.Background(), subprocess.MCPCallRequest{
		ToolName:  cerbplugin.ToolNameForOperation(ConnectorID, operation),
		Arguments: args,
	})
	return err
}

// The host refuses an unacknowledged Destructive operation before it reaches
// the plugin. This is the plugin's own floor under that, for a caller that is
// not the host: no write runs without acknowledgment, and none reaches the
// backend.
func TestWritesRefuseToRunUnacknowledged(t *testing.T) {
	cases := map[string]map[string]any{
		"scale_workload":   {"kind": "deployment", "name": "web", "replicas": float64(2)},
		"restart_workload": {"kind": "deployment", "name": "web"},
		"cordon_node":      {"node": "worker-1"},
		"uncordon_node":    {"node": "worker-1"},
		"delete_pod":       {"pod": "web-1"},
	}
	if len(cases) != len(writeOperations) {
		t.Fatalf("this test covers %d writes, writeOperations has %d", len(cases), len(writeOperations))
	}
	for operation, args := range cases {
		fake := &FakeBackend{}
		p := newTestPlugin(t, fake, nil)
		err := callErr(p, operation, args)
		if err == nil || !strings.Contains(err.Error(), "requires acknowledgment") {
			t.Errorf("%s unacknowledged: err = %v", operation, err)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("%s reached the backend unacknowledged: %v", operation, fake.Calls)
		}
		assertSurvivesRedaction(t, operation+" refusal", err.Error())
	}
}

func TestDryRunNeedsNoAcknowledgmentAndReachesTheBackend(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)

	call(t, p, "scale_workload", map[string]any{"kind": "sts", "name": "db", "replicas": float64(0), "dry_run": true})
	if !fake.LastScale.DryRun || fake.LastScale.Replicas != 0 || fake.LastScale.Kind != "sts" {
		t.Errorf("scale request = %+v", fake.LastScale)
	}

	call(t, p, "cordon_node", map[string]any{"node": "worker-1", "dry_run": true})
	if !fake.LastNode.DryRun || fake.LastNode.Schedulable {
		t.Errorf("cordon request = %+v", fake.LastNode)
	}
	call(t, p, "uncordon_node", map[string]any{"node": "worker-1", "dry_run": true})
	if !fake.LastNode.Schedulable {
		t.Errorf("uncordon request = %+v, want schedulable", fake.LastNode)
	}
}

// Acknowledged, not dry run: a real write, and the backend is told so.
func TestAcknowledgedWriteIsNotADryRun(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, map[string]string{ConfigNamespace: "apps"})
	call(t, p, "restart_workload", map[string]any{"kind": "deployment", "name": "web", "acknowledged": true})
	if fake.LastRestart.DryRun {
		t.Error("an acknowledged write was sent as a dry run")
	}
	if fake.LastRestart.Namespace != "apps" {
		t.Errorf("namespace = %q, want the configured default", fake.LastRestart.Namespace)
	}
}

// The CLI sends --arg replicas=3 as a string, and the host sends --dry-run as a
// JSON bool. Both have to land.
func TestWriteArgumentsAcceptTheCLIStringTyping(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)
	call(t, p, "scale_workload", map[string]any{"kind": "deployment", "name": "web", "replicas": "3", "acknowledged": "true"})
	if fake.LastScale.Replicas != 3 || fake.LastScale.DryRun {
		t.Errorf("scale request = %+v", fake.LastScale)
	}
	call(t, p, "delete_pod", map[string]any{"pod": "web-1", "grace_period_seconds": "0", "dry_run": true})
	if fake.LastDelete.GracePeriod == nil || *fake.LastDelete.GracePeriod != 0 {
		t.Errorf("grace period = %v, want an explicit zero", fake.LastDelete.GracePeriod)
	}
	call(t, p, "delete_pod", map[string]any{"pod": "web-1", "dry_run": true})
	if fake.LastDelete.GracePeriod != nil {
		t.Errorf("grace period = %v, want nil so the pod's own applies", *fake.LastDelete.GracePeriod)
	}
}

// A missing replica count must not become zero: that would scale a workload
// to nothing because an argument was forgotten. Nor may 2.5 become 2.
func TestScaleRejectsAMissingOrFractionalReplicaCount(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"missing":    {"kind": "deployment", "name": "web", "dry_run": true},
		"empty":      {"kind": "deployment", "name": "web", "replicas": "", "dry_run": true},
		"fractional": {"kind": "deployment", "name": "web", "replicas": 2.5, "dry_run": true},
		"garbage":    {"kind": "deployment", "name": "web", "replicas": "three", "dry_run": true},
	} {
		fake := &FakeBackend{}
		p := newTestPlugin(t, fake, nil)
		if err := callErr(p, "scale_workload", args); err == nil {
			t.Errorf("%s replicas accepted", name)
		}
		if len(fake.Calls) != 0 {
			t.Errorf("%s replicas reached the backend", name)
		}
	}
}

func TestNewReadsPassTheirScopingThrough(t *testing.T) {
	fake := &FakeBackend{}
	p := newTestPlugin(t, fake, nil)

	call(t, p, "describe_workload", map[string]any{"kind": "ds", "name": "agent", "namespace": "kube-system"})
	if fake.LastRef != (WorkloadRef{Namespace: "kube-system", Kind: "ds", Name: "agent"}) {
		t.Errorf("describe ref = %+v", fake.LastRef)
	}
	call(t, p, "list_services", map[string]any{"all_namespaces": "true", "selector": "app=web", "limit": "5"})
	if !fake.LastScoped.AllNamespaces || fake.LastScoped.Selector != "app=web" || fake.LastScoped.Limit != 5 {
		t.Errorf("services query = %+v", fake.LastScoped)
	}
	call(t, p, "list_api_resources", map[string]any{"group": "apps"})
	if fake.LastAPI.Group != "apps" || fake.LastAPI.Limit != DefaultAPIResourceLimit {
		t.Errorf("api query = %+v", fake.LastAPI)
	}
	call(t, p, "top", map[string]any{"kind": "pods", "namespace": "apps"})
	if fake.LastTop.Kind != "pods" || fake.LastTop.Namespace != "apps" {
		t.Errorf("top query = %+v", fake.LastTop)
	}
}

func TestDescribeWorkloadReportsEveryMissingArgumentAtOnce(t *testing.T) {
	err := callErr(newTestPlugin(t, &FakeBackend{}, nil), "describe_workload", nil)
	if err == nil || !strings.Contains(err.Error(), "kind is required") || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("err = %v", err)
	}
}
