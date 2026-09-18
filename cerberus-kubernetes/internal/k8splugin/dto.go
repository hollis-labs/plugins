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
// Every DTO here is read-only by construction. There is no write DTO because
// there are no write operations — see WP-K6 in
// docs/plans/k8s-connector-plugin.md for what would unlock them.

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
	User string `json:"user,omitempty"`

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
	CredentialPlugin *CredentialPlugin `json:"credential_plugin,omitempty"`

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
	Message string `json:"message,omitempty"`

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
	Message string `json:"message,omitempty"`

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
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated,omitempty"`
}
