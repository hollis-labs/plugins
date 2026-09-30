# vercel

Cerberus connector for Vercel deployments. It runs a deployment profile you
define, on the machine Cerberus runs on: the profile's preflight and build
commands, `vercel link` when the repo is not linked yet, then its deploy
command. The dry run shows every step before anything runs.

> **Pre-release — v0.1.0.** This plugin replaces the deployment-profile runner
> Cerberus used to compile in (`internal/infra`), with the same planning and
> the same secret names. It has been tested against a fake Vercel CLI and a
> throwaway host. No outside users, no compatibility guarantees, no support
> channel. Built in the open: interfaces and behaviour can change without
> notice.

## Operations

| Operation | Effect | Acknowledgment | Dry run | Notes |
|---|---|---|---|---|
| `status` | read | no | no | Whether the Vercel CLI resolves, whether a token and a default scope are configured, and whether the profiles file parses. No network call. |
| `list_profiles` | read | no | no | The profiles: id, name, repo path, project, scope, domain, and whether the repo is linked. Not their commands. |
| `deploy` | exec, writes locally | `--ack` | yes | `profile`. Runs the profile's steps in its `repo_path`. |

```bash
cerberus connectors exec vercel list_profiles
cerberus connectors exec vercel deploy --arg profile=site --dry-run --ack
cerberus connectors exec vercel deploy --arg profile=site --ack
```

### What `deploy` runs

The caller names a profile and nothing else. **What runs is chosen by the
profiles file, which only the operator edits.** An agent with access to
`deploy` can run the commands you wrote, and cannot add any. In order:

1. `preflight_command`, if set, in `/bin/sh -lc`.
2. `build_command`, if set, the same way.
3. `vercel link --yes --project <vercel_project> [--scope <scope>]`, when the
   repo has no `.vercel/project.json`. A repo that is not linked needs
   `vercel_project`.
4. `deploy_command`, default `vercel --prod --yes`, with `--scope <scope>`
   added unless it already names one.

The scope is the profile's `vercel_scope`, or else the `scope` secret. The
token reaches the link and deploy steps only as `VERCEL_TOKEN` in their
environment, never on a command line, where every local user could read it
through `ps`. Plans and results show it as `VERCEL_TOKEN=<vercel token>`.

The first step that fails stops the run. The result lists each step's command
and output (its last 64 KiB), the checkout's branch, commit and dirty state,
and the deployment URL read from the deploy step's output.

### The dry run

The dry run runs nothing. It returns the host's usual preview (summary,
target, input, warnings) plus:

| Field | What |
|---|---|
| `steps` | Each step's name, displayed command, and the names of the environment variables it gets. |
| `repo_path` | Where the steps run. |
| `profile_sha256` | A digest of the profile. |
| `git` | The checkout's branch, commit and dirty state. |

Cerberus binds an approval's plan hash to this preview. **A profile or
checkout that changes between the approval and the run makes the approval
stale.** Like every plugin preview it is this plugin's claim: the host cannot
verify it, and the real call plans again when it starts rather than running
the exact steps that were approved. The compiled-in runner did run the exact
plan the gate checked. That guarantee does not cross the plugin boundary yet.

## Configure

### Profiles

Profiles live in a YAML file you edit, named by the `profiles_file` field in
`~/.cerberus/connector-config.yaml`:

```yaml
vercel:
  fields:
    profiles_file: ~/.cerberus/vercel-profiles.yaml
  limits:
    operations:
      deploy: 20m      # the default call deadline is 2m
    memory_mib: 8192   # the watchdog covers the build's processes too
```

```yaml
# ~/.cerberus/vercel-profiles.yaml
profiles:
  - id: site
    name: Marketing site
    repo_path: /Users/me/src/site
    vercel_project: site
    vercel_scope: my-team          # optional; else the scope secret
    git_remote: origin             # optional; reported in the result
    preflight_command: pnpm lint   # optional
    build_command: pnpm build      # optional
    deploy_command: vercel --prod --yes   # optional; this is the default
```

The file is read on every call, so an edit applies to the next call without a
reload. It is the shape Cerberus's own `infra.yaml` used, and keys this plugin
does not use are ignored, so a file written for it reads unchanged.

**Raise the limits for real builds.** A plugin call has a 2-minute deadline
and a 2 GiB memory watchdog over the plugin's whole process group by default,
and a build's child processes count against both. When the deadline passes,
the plugin kills the step's whole process group.

### Labels

A profile is the target of `deploy` (`vercel.profile`, from `profile`).
Cerberus labels a target from the `config.yaml` resource with the same id, so
give each profile a resource that names its environment and owner:

```yaml
resources:
  - id: site
    type: deploy_profile
    connector: vercel
    env: prod
    owner: me
```

Without one, the target is unknown, which Cerberus treats as strictly as
production: out-of-band approval wherever policy asks for approval. Labels in
the profiles file are ignored, since that file is not where Cerberus looks.

### Secrets

| Secret | Kind | Notes |
|---|---|---|
| `token` | credential | Optional. Without it, the Vercel CLI uses its own login session (`vercel login`). |
| `scope` | name | Optional default team scope. |

Supply them the usual ways: `CERBERUS_VERCEL_TOKEN`, a `vercel:` entry in
`connector-secrets.yaml`, or `keychain://vercel/token`. These are the names
the compiled-in runner read, so existing entries keep working. The plugin
removes the token from every result and error it returns, including a step's
output that echoes it.

### The Vercel CLI

The link step runs the CLI by path, resolved on each call from `PATH`, then
`/opt/homebrew/bin`, `/usr/local/bin`, and the global bin directories of npm,
pnpm, bun and volta under your home. The daemon runs with launchd's minimal
`PATH` unless its plist carries one. The deploy command runs in a login
shell, so it sees your shell profile's `PATH`.

## Errors

Every refusal is coded `invalid_args` and says what to fix: no profiles file
configured, an unknown profile, a profile for another provider, a missing
`repo_path` or `vercel_project`, the CLI not found, or an unacknowledged
deploy. A deploy that ran and failed is not an error. It returns its result
with `success: false`, the failing step's output, and `error`.
