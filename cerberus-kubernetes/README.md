# cerberus-kubernetes-plugin

Read-only Kubernetes cluster inspection for Cerberus.

> **Pre-release — v0.1.0.** Nine read-only operations, verified end to end
> against a `kind` cluster running Kubernetes v1.37.0 (matching client-go
> v0.37.0), both over the plugin protocol directly and through an installed
> Cerberus daemon. **Not yet verified:** the `exec` credential-plugin path
> (kind authenticates with a client certificate, so the PATH resolution and
> interactive-mode refusal are unit-tested only), and behavior under a
> restricted RBAC role. No write, scale, delete or exec operations exist — see
> [Locked operations](#locked-operations). No outside users, no compatibility
> guarantees, no support channel. Built in the open: interfaces and behavior
> can change without notice.

Clusters differ in how they authenticate, and the answer decides more than it
looks like it should. So rather than assume one mechanism, every plausible
answer is a named mode in `auth.go`, the plugin classifies whichever one it is
handed, and `check_access` reports the classification before anything is
attempted.

## Operations

All read-only. None is `Destructive`, none declares `SupportsDry`; writes are
locked with their unlock conditions in WP-K6.

| Operation | Needs a cluster? |
|---|---|
| `list_contexts` | no — reads the kubeconfig |
| `check_access` | no — reads the kubeconfig and the local filesystem |
| `get_health` | yes |
| `list_namespaces` | yes |
| `list_nodes` | yes |
| `list_pods` | yes |
| `list_workloads` | yes |
| `list_events` | yes |
| `get_logs` | yes |

`list_contexts` and `check_access` deliberately never touch the backend. They
are the two operations an operator needs *because* authentication is not
working, so a plugin that could not answer them without a cluster would be
useless at the only moment it mattered.

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
TTY. `interactiveMode: Always` is reported as a problem rather than attempted.
Retrying an interactive SSO login on a timer is a reliable way to get an account
locked out, which is the same reason Cerberus does not auto-start its SSH tunnel
resources.

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

## Locked operations

What is deliberately absent, and why:

- **No `get_secret`**, in any form, including keys-only. It is the one read
  where a DTO slip is unrecoverable, and nobody has asked for it.
- **No informers, no watch, no controller-runtime.** The admin lane is stateless
  and resolved per call; a reconcile loop against a remote cluster is the
  supervision lane, which does not reach remote systems by design.
- **No log streaming, no exec, no port-forward.** See WP-K5.
- **No Helm.** See WP-K7.

## DTOs

`dto.go` is an allow-list, per ADR 0003, and Kubernetes is the worst case that
ADR has faced: `Secret.Data`, `Pod.Spec.Containers[].Env`, the
`last-applied-configuration` annotation and an `ExecConfig`'s `Args` and `Env`
all carry live credentials. The rule throughout is names, never values —
`EnvNames`, never `Env`. `dto_test.go` populates every credential-shaped field
with one sentinel and asserts it appears nowhere in the marshalled output.

## Build

```bash
make test
make dist                     # from the repo root, or `make dist` here
cerberus connectors plugin managed install "$PWD/../dist/kubernetes"
cerberus connectors plugin managed load kubernetes
cerberus connectors plugin managed exec kubernetes list_contexts
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

kind authenticates with a client certificate, which is useful in itself — it
proves the daemon's minimal `PATH` is only a problem for the `exec` modes. It
also means the `exec` credential path is still unit-tested only.

If you are pointing this at a cluster whose kubeconfig uses an `exec` credential
plugin, run `check_access` first. It is the operation that tells you whether the
helper resolves from where the daemon actually runs, which is not the same place
your shell runs.
