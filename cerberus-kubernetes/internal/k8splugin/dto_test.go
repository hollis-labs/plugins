package k8splugin

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// sentinel is the value every credential-shaped field in these tests is set to.
// If it appears anywhere in a marshalled DTO, the allow-list has a hole.
const sentinel = "S3CRET-CANARY-VALUE"

// This is the assertion ADR 0003 exists for. The natural implementation of
// list_pods returns corev1.Pod, and corev1.Pod carries every environment
// variable a container was given — which in practice is where application
// credentials live. A DTO that leaked them would put them into CLI stdout,
// daemon logs, MCP tool results and an agent's context window at once.
func TestPodDTOEmitsEnvironmentNamesButNeverValues(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-abc123",
			Namespace: "apps",
			// last-applied-configuration is routinely a verbatim copy of the
			// original manifest, credentials included, on an object whose live
			// spec looks clean. No DTO field carries annotations, and this
			// asserts that stays true.
			Annotations: map[string]string{
				"kubectl.kubernetes.io/last-applied-configuration": `{"env":[{"name":"DB_PASSWORD","value":"` + sentinel + `"}]}`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-1",
			Containers: []corev1.Container{{
				Name:  "web",
				Image: "registry.example.com/web:1.2.3",
				Env: []corev1.EnvVar{
					{Name: "DB_PASSWORD", Value: sentinel},
					{Name: "API_TOKEN", Value: sentinel},
				},
				EnvFrom: []corev1.EnvFromSource{
					{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "web-secrets"}}},
					{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "web-config"}}},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "web",
				Ready:        true,
				RestartCount: 2,
				State:        corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
			}},
		},
	}

	dto := mapPod(pod)
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal pod DTO: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("pod DTO leaked a credential value: %s", encoded)
	}

	container := dto.Containers[0]
	if got := strings.Join(container.EnvNames, ","); got != "DB_PASSWORD,API_TOKEN" {
		t.Errorf("env names = %q, want the names to survive", got)
	}
	if got := strings.Join(container.EnvFromNames, ","); got != "secret/web-secrets,configmap/web-config" {
		t.Errorf("envFrom names = %q", got)
	}
	if dto.Ready != "1/1" || dto.Restarts != 2 {
		t.Errorf("ready=%q restarts=%d, want 1/1 and 2", dto.Ready, dto.Restarts)
	}
}

// An exec credential plugin's Args and Env are where a client secret is
// configured. The AccessCheck DTO reports that a helper exists and whether it
// resolves; it must never report how to authenticate as anybody.
func TestCredentialPluginDTOExcludesArgsAndEnvValues(t *testing.T) {
	execCfg := &clientcmdapi.ExecConfig{
		Command:         "some-credential-helper",
		Args:            []string{"get-token", "--client-secret=" + sentinel},
		Env:             []clientcmdapi.ExecEnvVar{{Name: "AAD_CLIENT_SECRET", Value: sentinel}},
		InstallHint:     "install it from the internal package repo",
		InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
	}

	plugin, _ := inspectCredentialPlugin(execCfg, "")
	encoded, err := json.Marshal(plugin)
	if err != nil {
		t.Fatalf("marshal credential plugin DTO: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("credential plugin DTO leaked a secret: %s", encoded)
	}
	if strings.Contains(string(encoded), "get-token") {
		t.Fatalf("credential plugin DTO emitted exec args, which can carry secrets: %s", encoded)
	}
	if len(plugin.EnvNames) != 1 || plugin.EnvNames[0] != "AAD_CLIENT_SECRET" {
		t.Errorf("env names = %v, want the name to survive", plugin.EnvNames)
	}
}

// A kubeconfig auth-info holds static tokens, passwords and client key data.
// Nothing in the Context DTO may carry them: it reports the auth-info *name*
// and the classified mode only.
func TestContextDTOCarriesNoCredentialFromTheKubeconfig(t *testing.T) {
	dir := t.TempDir()
	path := writeKubeconfig(t, dir)

	contexts, err := Contexts(ClusterOptions{Kubeconfig: path})
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	encoded, err := json.Marshal(contexts)
	if err != nil {
		t.Fatalf("marshal contexts: %v", err)
	}
	if strings.Contains(string(encoded), sentinel) {
		t.Fatalf("context DTO leaked a credential: %s", encoded)
	}
}
