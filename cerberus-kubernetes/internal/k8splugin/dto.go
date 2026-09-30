package k8splugin

// The types in this file are the security boundary required by
// docs/adr/0003-connector-response-dtos.md in the Cerberus repo.
//
// Kubernetes is the worst case that ADR has faced. Returning an upstream type
// emits, into CLI stdout, daemon logs, MCP tool results and an agent's context
// window at once:
//
//   - corev1.Secret.Data — the credential values themselves. Base64 is an
//     encoding, not redaction.
//   - corev1.Pod.Spec.Containers[].Env — literal values, which in practice is
//     where application credentials actually live.
//   - the kubectl.kubernetes.io/last-applied-configuration annotation — often a
//     verbatim copy of the original manifest, env values included, sitting on
//     an object whose live spec looks clean.
//   - clientcmdapi.AuthInfo.Token / ClientKeyData / Password, and an
//     ExecConfig's Args and Env, which routinely carry a client secret.
//
// So these DTOs are an allow-list. A field upstream adds in a minor release is
// not emitted unless someone adds it here on purpose. The rule throughout is
// the `probe-*` convention from ~/admin-tools: **names, never values**, so
// output is safe to paste into a document or hand to an agent.
//
// The read DTOs are here; the write operations' result type is in
// dto_writes.go, and it follows the same rule: it reports which fields changed
// and to what, from an allow-list of fields that carry no credential.

// Health reports whether the API server answered, and as whom.
type Health struct {
	Reachable bool   `json:"reachable"`
	Context   string `json:"context,omitempty"`
	Server    string `json:"server,omitempty"`
	Version   string `json:"version,omitempty"`
	Platform  string `json:"platform,omitempty"`

	// Message names what actually failed. A refused dial on a VPN-only API
	// server means the VPN is down, not the cluster; an expired credential is
	// not an RBAC denial. Telling an operator the wrong one sends them to the
	// wrong system.
	Message string `json:"message,omitempty"`
}

// Context is one kubeconfig context. Reading these needs no cluster contact,
// which makes it the one operation that works before anything else does.
type Context struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster,omitempty"`
	Server    string `json:"server,omitempty"`
	Namespace string `json:"namespace,omitempty"`

	// User is the kubeconfig auth-info *name*, never its contents.
	User string `json:"user,omitempty" cerb:"personal"`

	// AuthMode is how this context authenticates, classified by auth.go. It is
	// the answer to WP-K0 for one context, readable without asking anyone.
	AuthMode  string `json:"auth_mode"`
	IsCurrent bool   `json:"is_current"`
}

// AccessCheck is the preflight: what would happen if we tried to use this
// context from where the plugin is actually running.
//
// This exists because two of Cerberus's documented outages live in the exec
// credential-plugin path — the daemon's minimal PATH, and the absence of a TTY
// for an interactive login. Both are detectable before a call is made, and a
// named cause beats a 401 every time.
type AccessCheck struct {
	Context  string `json:"context"`
	Server   string `json:"server,omitempty"`
	AuthMode string `json:"auth_mode"`

	// Ready is true when a request could be attempted. It is not a claim that
	// the credential is valid — only that nothing local prevents the attempt.
	Ready bool `json:"ready"`

	// CredentialPlugin is populated only for AuthModeExec.
	//
	// Its key is "exec_helper", not "credential_plugin". The host's output
	// redaction hides every string beneath a key containing CREDENTIAL, so under
	// the old key the helper's command, resolved path, install hint and env
	// names all reached the operator as [REDACTED] — the whole diagnosis
	// check_access exists to give. Guarded by TestNoDTOKeyIsOneTheHostRedacts.
	CredentialPlugin *CredentialPlugin `json:"exec_helper,omitempty"`

	// Problems are operator-facing and each one names its recovery. They are
	// composed from fixed strings and already-safe names, never from a
	// credential, so redact.Text on the host has nothing to eat.
	Problems []string `json:"problems,omitempty"`
}

// CredentialPlugin describes an exec credential helper without describing how
// to authenticate as anybody.
type CredentialPlugin struct {
	// Command is the binary name from the kubeconfig — a name, not a secret.
	Command string `json:"command"`

	// Resolved reports whether that binary was found from this process, and
	// ResolvedPath where. Under launchd the daemon's PATH is
	// /usr/bin:/bin:/usr/sbin:/sbin, which holds none of kubelogin, az, aws or
	// gke-gcloud-auth-plugin.
	Resolved     bool   `json:"resolved"`
	ResolvedPath string `json:"resolved_path,omitempty"`

	// InstallHint is the kubeconfig's own installHint, passed through so the
	// operator gets the cluster owner's instructions rather than ours.
	InstallHint string `json:"install_hint,omitempty"`

	// InteractiveMode is "Never", "IfAvailable" or "Always". Always cannot work
	// under the daemon: a Cerberus-launched process has no TTY, which is the
	// same reason the SSH tunnel resources are deliberately not auto-started.
	InteractiveMode string `json:"interactive_mode,omitempty"`

	// EnvNames lists the names of environment variables the kubeconfig sets for
	// the helper. Never the values: this is exactly where a client secret is
	// configured. Args are not emitted at all, for the same reason.
	EnvNames []string `json:"env_names,omitempty"`
}

// Namespace is the Cerberus view of a namespace.
type Namespace struct {
	Name    string `json:"name"`
	Status  string `json:"status,omitempty"`
	Age     string `json:"age,omitempty"`
	Created string `json:"created,omitempty"`
}

// Node is the Cerberus view of a cluster node.
type Node struct {
	Name          string   `json:"name"`
	Ready         bool     `json:"ready"`
	Status        string   `json:"status,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	Version       string   `json:"version,omitempty"`
	OS            string   `json:"os,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	Unschedulable bool     `json:"unschedulable"`

	// Pressures are the condition names currently true other than Ready —
	// MemoryPressure, DiskPressure, PIDPressure. The usual first answer to
	// "why is this node misbehaving".
	Pressures []string `json:"pressures,omitempty"`
	Age       string   `json:"age,omitempty"`
}

// Pod is the Cerberus view of a pod: what an operator reads in a list, not a
// hundred fields of cluster internals.
type Pod struct {
	Name       string      `json:"name"`
	Namespace  string      `json:"namespace"`
	Phase      string      `json:"phase,omitempty"`
	Ready      string      `json:"ready,omitempty"` // "1/2"
	Restarts   int32       `json:"restarts"`
	Node       string      `json:"node,omitempty"`
	Age        string      `json:"age,omitempty"`
	Owner      string      `json:"owner,omitempty"` // "ReplicaSet/web-5d4f"
	Containers []Container `json:"containers,omitempty"`
}

// Container is the per-container view. Note what is absent: Env.
type Container struct {
	Name         string `json:"name"`
	Image        string `json:"image,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restart_count"`
	State        string `json:"state,omitempty"`

	// Reason and Message carry CrashLoopBackOff, ImagePullBackOff and friends,
	// which is the whole reason to look at a container at all.
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty" cerb:"untrusted"`

	// EnvNames is the allow-list rendering of Spec.Containers[].Env: the names
	// only. An operator can see that DATABASE_PASSWORD is set without the
	// value crossing into a log, a transcript or a model's context.
	EnvNames []string `json:"env_names,omitempty"`

	// EnvFromNames lists the ConfigMap and Secret names an envFrom pulls in —
	// again names, and again never the contents.
	EnvFromNames []string `json:"env_from_names,omitempty"`
}

// Workload is the Cerberus view of a Deployment, StatefulSet or DaemonSet.
type Workload struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`

	Desired   int32 `json:"desired"`
	Ready     int32 `json:"ready"`
	Updated   int32 `json:"updated,omitempty"`
	Available int32 `json:"available,omitempty"`

	Images []string `json:"images,omitempty"`
	Age    string   `json:"age,omitempty"`

	// Condition is the summarised rollout state, e.g. "Available" or
	// "Progressing: ReplicaSetUpdated".
	Condition string `json:"condition,omitempty"`
}

// Event is the Cerberus view of a cluster event — the highest-value read for
// "why is this broken".
type Event struct {
	Namespace string `json:"namespace,omitempty"`
	Type      string `json:"type,omitempty"` // Normal | Warning
	Reason    string `json:"reason,omitempty"`
	Object    string `json:"object,omitempty"` // "Pod/web-5d4f-abcde"

	// Message is the event text as the cluster wrote it. This is the one field
	// here that Cerberus does not compose, so it is also the one that could
	// carry something a component logged carelessly. It is kept because without
	// it the operation has no value.
	Message string `json:"message,omitempty" cerb:"untrusted"`

	Count     int32  `json:"count,omitempty"`
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// LogSnapshot is a bounded read of a container's logs, never a stream. The
// admin lane returns a result; `cerberus resource logs` already sets this
// shape.
type LogSnapshot struct {
	Namespace string   `json:"namespace"`
	Pod       string   `json:"pod"`
	Container string   `json:"container,omitempty"`
	Previous  bool     `json:"previous,omitempty"`
	Lines     []string `json:"lines" cerb:"untrusted"`
	Truncated bool     `json:"truncated,omitempty"`
}

// WorkloadDetail is describe_workload's answer: the useful subset of `kubectl
// describe`, not its full dump. It deliberately carries no annotations and no
// pod template metadata, which is where last-applied-configuration lives.
type WorkloadDetail struct {
	Workload

	// Selector is the label selector rendered as a string, e.g. "app=web".
	// Labels are names and values an operator chose to make visible; they are
	// not where credentials live.
	Selector string `json:"selector,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	Paused   bool   `json:"paused,omitempty"`

	Conditions []Condition     `json:"conditions,omitempty"`
	Containers []ContainerSpec `json:"containers,omitempty"`

	// Volumes are "kind/name" references — "secret/web-tls", "pvc/data",
	// "emptyDir/cache" — never the contents of what they mount.
	Volumes []string `json:"volumes,omitempty"`

	// Pods are the pods the selector currently matches, bounded.
	Pods List[Pod] `json:"pods"`

	// Events are the recent events recorded against this workload object
	// itself, newest last. Pod-level events are on the pods; list_events with a
	// namespace reads those.
	Events []Event `json:"events,omitempty"`
}

// Condition is one status condition, as the controller wrote it.
type Condition struct {
	Type           string `json:"type"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty" cerb:"untrusted"`
	LastTransition string `json:"last_transition,omitempty"`
}

// ContainerSpec is the template's view of a container: what it is configured
// to be, rather than what a running instance is doing. Note, again, what is
// absent: Env values, Args and Command, any of which can carry a credential.
type ContainerSpec struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`

	// Ports render as "8080/TCP" or "http:8080/TCP".
	Ports []string `json:"ports,omitempty"`

	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`

	// Probes lists which of liveness, readiness and startup are configured.
	// Whether a probe exists is usually the question; its exec command could
	// carry anything, so it is not emitted.
	Probes []string `json:"probes,omitempty"`

	EnvNames     []string `json:"env_names,omitempty"`
	EnvFromNames []string `json:"env_from_names,omitempty"`
}

// Service is the Cerberus view of a Service.
type Service struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Type      string `json:"type,omitempty"`
	ClusterIP string `json:"cluster_ip,omitempty"`

	// ExternalAddresses merges external IPs and load-balancer ingress, which is
	// the "where do I reach this from outside" answer whichever way it is set.
	ExternalAddresses []string `json:"external_addresses,omitempty"`

	// Ports render as "http 80→8080/TCP", with ":30080" appended for a node port.
	Ports    []string `json:"ports,omitempty"`
	Selector string   `json:"selector,omitempty"`
	Age      string   `json:"age,omitempty"`
}

// Ingress is the Cerberus view of an Ingress.
type Ingress struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Class     string `json:"class,omitempty"`

	// Routes render as "host/path → service:port".
	Routes    []string     `json:"routes,omitempty"`
	TLS       []IngressTLS `json:"tls,omitempty"`
	Addresses []string     `json:"addresses,omitempty"`
	Age       string       `json:"age,omitempty"`
}

// IngressTLS names the secret an Ingress terminates TLS with. The name only:
// the secret holds the private key.
//
// The key is "certificate", holding "secret/<name>" in the same form as
// env_from_names, and not "secret_name". The host redacts the value of any key
// containing SECRET, so "secret_name" reached the operator as "[REDACTED]" —
// found against a real cluster, and now guarded by
// TestNoDTOKeyIsOneTheHostRedacts.
type IngressTLS struct {
	Hosts       []string `json:"hosts,omitempty"`
	Certificate string   `json:"certificate,omitempty"`
}

// APIResource is one resource type the API server serves — the answer to "what
// does this cluster even have", including every CRD someone else installed.
type APIResource struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Group      string   `json:"group,omitempty"` // empty is the core group
	Version    string   `json:"version"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs,omitempty"`
	ShortNames []string `json:"short_names,omitempty"`
}

// APIResourceList is a bounded discovery answer. Discovery can partially fail —
// an aggregated API server that is down fails its own group and nothing else —
// so FailedGroups says which groups are missing rather than letting a partial
// list pass for a complete one.
type APIResourceList struct {
	List[APIResource]
	FailedGroups []string `json:"failed_groups,omitempty"`
}

// Usage is top's answer. Available is false, with a Reason, when the cluster
// serves no metrics API: most clusters without metrics-server installed. That
// is a normal state, not an error, so it is reported rather than raised.
type Usage struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Kind      string `json:"kind"` // nodes | pods
	Namespace string `json:"namespace,omitempty"`

	// Items are sorted by CPU, highest first.
	Items     []UsageItem `json:"items"`
	Count     int         `json:"count"`
	Truncated bool        `json:"truncated,omitempty"`
}

// UsageItem is one node's or pod's current consumption.
type UsageItem struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	CPU       string `json:"cpu"`    // "250m"
	Memory    string `json:"memory"` // "128Mi"

	// CPUPercent and MemoryPercent are against allocatable, for nodes only.
	CPUPercent    *int `json:"cpu_percent,omitempty"`
	MemoryPercent *int `json:"memory_percent,omitempty"`
}
