# cerberus-kubernetes-plugin

Kubernetes cluster inspection and administration for Cerberus.

> **Pre-release — v0.2.0.** Fourteen read operations and five write
> operations, verified end to end against a `kind` cluster running Kubernetes
> v1.37.0 (matching client-go v0.37.0) through Cerberus's plugin host, under the
> daemon's minimal `PATH`: the `exec` credential-plugin path with a helper
> found outside that `PATH`, a restricted read-only role, and every write both
> as a server-side dry run and for real. **Not yet verified:** a managed
> cluster's own credential helper (`kubelogin`, `aws eks get-token`,
> `gke-gcloud-auth-plugin`) — a stand-in helper was used — and any cluster
> larger than one node. No outside users, no compatibility guarantees, no
> support channel. Built in the open: interfaces and behavior can change
> without notice.

Clusters differ in how they authenticate, and the answer decides more than it
looks like it should. So rather than assume one mechanism, every plausible
answer is a named mode in `auth.go`, the plugin classifies whichever one it is
handed, and `check_access` reports the classification before anything is
attempted.

## Operations

### Reads

Each declares effect `read`, except `get_logs` and `list_events`, which are `read_sensitive`: they return text the cluster wrote, which can carry secrets or personal data. No read asks for `--ack`.

| Operation | Needs a cluster? | Notes |
|---|---|---|
| `list_contexts` | no — reads the kubeconfig | |
| `check_access` | no — reads the kubeconfig and the local filesystem | run this first when something is wrong |
| `get_health` | yes | |
| `list_namespaces` | yes | |
| `list_nodes` | yes | |
| `list_pods` | yes | |
| `list_workloads` | yes | deployments, statefulsets, daemonsets |
| `list_events` | yes | |
| `get_logs` | yes | bounded snapshot; see the warning below |
| `describe_workload` | yes | conditions, strategy, containers, owned pods, the workload's own events |
| `list_services` | yes | |
| `list_ingresses` | yes | routes, and the *name* of each TLS secret |
| `list_api_resources` | yes | every type the server serves, CRDs included; names any API group that failed discovery |
| `top` | yes, plus metrics-server | reports `available: false` with the reason when there is none |

`list_contexts` and `check_access` deliberately never touch the backend. They
are the two operations an operator needs *because* authentication is not
working, so a plugin that could not answer them without a cluster would be
useless at the only moment it mattered.

`get_logs` returns log content as the application wrote it. Applications
routinely log their own configuration at startup, so treat the output as
sensitive before handing it to an agent.

### Writes

| Operation | What it does |
|---|---|
| `scale_workload` | sets a deployment's or statefulset's replicas, through the `scale` subresource |
| `restart_workload` | rolling restart, the way `kubectl rollout restart` does it |
| `cordon_node` / `uncordon_node` | marks a node unschedulable or schedulable again; not a drain |
| `delete_pod` | deletes one pod, and says whether anything will recreate it |

Every write declares effect `lifecycle` (scale, restart, cordon, uncordon) or `destructive` (`delete_pod`), with a `server` preview:

- **The host refuses it without `--ack`.** The plugin refuses an
  unacknowledged write too, so the rule holds for a caller that is not the host.
- **`--dry-run` is the API server's own dry run** (`dryRun=All`). Admission,
  validation, defaulting and RBAC all run; nothing is persisted. A dry run from
  an identity that may not make the change fails exactly as the real call
  would, so a preview cannot pass where the write would not.
- **Under the host, `--dry-run` also needs `--ack`.** The host cannot tell
  whether a plugin honours `dry_run`, so it gates the call either way. That is
  the host's policy, not this plugin's.
- **Every write is recorded as `cerberus-kubernetes-plugin`** in the object's
  `managedFields`, so a change made through Cerberus can be told apart from
  someone's `kubectl` afterwards.
- **The result is a `Change`, not the object.** It lists the fields touched,
  before and after, plus warnings such as "no controller owns this pod",
  "scaling to zero", or "this deployment is paused, so nothing will roll". A
  no-op, like scaling to the current count or cordoning a cordoned node, sends
  nothing and says so.

Whether a particular cluster *should* accept writes from Cerberus is a policy
question about that cluster. The connector does not answer it. Point it at an
identity whose RBAC says what it may do. The dry run will tell you before
anything changes.

```bash
cerberus connectors exec kubernetes scale_workload \
  --arg kind=deployment --arg name=web --arg replicas=3 --dry-run --ack
cerberus connectors exec kubernetes scale_workload \
  --arg kind=deployment --arg name=web --arg replicas=3 --ack
```

### Namespaces

A namespaced operation uses, in order: its own `namespace` argument, the
connector's configured `namespace`, the kubeconfig context's namespace, then
`default`. This is what kubectl does, plus the connector's own setting. The
first build skipped the context's namespace. That did not show until a write
against a real cluster missed its target, and a same-named workload in
`default` would have been the one scaled.

## Authentication modes

`RestConfig` chooses in a fixed order, and reports which mode it chose:

1. **`host-secret`** — a bearer token Cerberus resolved from its own secret
   provider plus an explicit `server` URL. No kubeconfig. Prefer this if the
   cluster will issue a ServiceAccount: non-interactive, attributable, and
   immune to both problems below.
2. **`in-cluster`** — a mounted ServiceAccount, detected rather than assumed.
3. **kubeconfig** — classified as `exec`, `token`, `token-file`,
   `client-certificate`, `basic`, `auth-provider` or `anonymous`.

### The two things that will actually break

Both are local, both are invisible in a 401, and `check_access` detects both
without contacting the cluster.

**The credential helper is not on the daemon's PATH.** launchd hands the
Cerberus daemon `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, which holds none of
`kubelogin`, `az`, `aws` or `gke-gcloud-auth-plugin`. `ResolveCredentialCommand`
searches wider, per call, and rewrites the resolved absolute path into an
in-memory copy of the kubeconfig before client-go execs it — the file on disk is
never modified. Add locations with `CERBERUS_KUBE_CREDENTIAL_PATH` or the
`credential_path` config field.

**The credential helper wants a terminal.** A Cerberus-launched process has no
TTY. With `interactiveMode: Always`, client-go refuses to run the helper at all,
before it could reuse a cached login. So `check_access` reports the context as
not ready, and the fix it names is `interactiveMode: IfAvailable`, not "log in
again". Retrying an interactive SSO login on a timer is a reliable way to get an
account locked out, which is the same reason Cerberus does not auto-start its
SSH tunnel resources.

## Arguments arrive typed two different ways

Over MCP, arguments are decoded JSON: `limit` is a number, `all_namespaces` is a
bool. Over the CLI, `--arg key=value` makes **everything a string**. `argMap` in
`plugin.go` accepts both, and rejects a value it cannot parse rather than
falling back to the default.

This is not a hypothetical. The first build handled only the JSON types, so
`--arg limit=2` silently returned the default page size and
`--arg all_namespaces=true` silently read as false — an operator asking for a
cluster-wide read got one namespace back with no indication the flag had been
dropped. Fake-backend tests cannot catch it, because they supply JSON types,
which is what the MCP path sends. If you add an argument here, assert both
typings.

## Not built

What is deliberately absent, and why:

- **No `get_secret`**, in any form, including keys-only. It is the one read
  where a DTO slip is unrecoverable, and nobody has asked for it.
- **No `drain`.** Draining evicts every pod on a node while respecting
  PodDisruptionBudgets, and it can stall on a budget indefinitely. That is a
  long-running, partially failing operation, not one call. `cordon_node` plus
  `delete_pod` covers the manual version.
- **No `apply`, and no Helm.** Arbitrary manifests are deployment, not
  administration, and they need the dynamic client and a review story of their
  own.
- **No informers, no watch, no controller-runtime.** The admin lane is stateless
  and resolved per call; a reconcile loop against a remote cluster is the
  supervision lane, which does not reach remote systems by design.
- **No log streaming, no exec, no port-forward.** These are interactive,
  long-lived sessions rather than request and response.

## DTOs

`dto.go` and `dto_writes.go` are an allow-list, per ADR 0003, and Kubernetes is
the worst case that ADR has faced: `Secret.Data`, `Pod.Spec.Containers[].Env`,
container `Command` and `Args`, probe `exec` commands, the
`last-applied-configuration` annotation and an `ExecConfig`'s `Args` and `Env`
all carry live credentials. The rule throughout is names, never values —
`EnvNames`, never `Env`; `secret/web-tls`, never its contents; which probes are
set, never what they run. The tests populate every credential-shaped field with
one sentinel and assert it appears nowhere in the marshalled output.

**A DTO key must not look like a credential to the host.** Cerberus redacts the
value of any JSON key containing `SECRET`, `TOKEN`, `CREDENTIAL` and similar,
and everything nested under it. Against a real cluster, that turned an
ingress's `secret_name` into `[REDACTED]`, and it would have blanked all of
`check_access`'s `credential_plugin` object: the helper's command, path and
install hint, which are the whole diagnosis. The keys are now `certificate` and
`exec_helper`. `TestNoDTOKeyIsOneTheHostRedacts` walks every DTO's JSON keys
against a copy of the host's rule.

## Build

```bash
make test
make dist                     # from the repo root, or `make dist` here
cerberus connectors plugin managed install "$PWD/../dist/kubernetes"
cerberus connectors plugin managed load kubernetes
cerberus connectors exec kubernetes list_contexts
```

Reinstalling after a rebuild needs the unload/install/load cycle, because the
host hashes the entrypoint at install time:

```bash
cerberus connectors plugin managed unload kubernetes
cerberus connectors plugin managed install "$PWD/../dist/kubernetes"
cerberus connectors plugin managed load kubernetes
```

## Testing against a real cluster

Verification runs against a throwaway `kind` cluster, so the suite needs no
access to anything you care about:

```bash
go install sigs.k8s.io/kind@latest
kind create cluster --name cerberus-probe     # Kubernetes v1.37.0, matches client-go v0.37.0
# ... exercise the operations ...
kind delete cluster --name cerberus-probe
```

`cerberus connectors plugin exec <dist-dir> <operation>` runs the plugin in
process, through the same host gating as the daemon, without installing it
into the daemon. Run it under `env -i PATH=/usr/bin:/bin:/usr/sbin:/sbin` to
get the daemon's environment rather than your shell's.

What `kind` alone does not cover, and how to cover it:

- **An `exec` credential helper.** kind authenticates with a client certificate.
  Add a context whose `exec` helper is a script in `~/.local/bin`, which is
  outside the minimal `PATH` but inside the plugin's fallback search, and have it
  print an `ExecCredential` carrying a ServiceAccount token
  (`kubectl create token`).
- **A restricted role.** Bind a ServiceAccount to the built-in `view` role and
  run the writes as it. Each should fail, dry run included, with the API
  server's own "cannot patch resource nodes" text.
- **`top`.** kind ships no metrics-server. The upstream manifest also needs
  `--kubelet-insecure-tls`. On a kind v1.37 node the pod may crash because it
  cannot write its self-signed certificate as uid 1000 into its `/tmp`
  emptyDir. Running it as root works on a throwaway cluster.

If you are pointing this at a cluster whose kubeconfig uses an `exec` credential
plugin, run `check_access` first. It is the operation that tells you whether the
helper resolves from where the daemon actually runs, which is not the same place
your shell runs.
