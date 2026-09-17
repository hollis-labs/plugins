# azure

Cerberus connector for Azure Resource Manager — read-only inventory, and the
model deployments an AI Services account actually serves.

## Scope: read and probe only

Cerberus is not taking over management of work infrastructure. An infrastructure
team administers the Azure estate; this connector helps operate it, it does not
own it. So every operation here is read-only: none is destructive, none takes
`--ack`, and a test asserts that rather than leaving it to review.

Write and lifecycle operations are listed under
[Not implemented, and why](#not-implemented-and-why) with what would unlock
them.

## Operations

| Operation | Arguments | Notes |
|---|---|---|
| `list_subscriptions` | — | Subscriptions this credential can see. Works before a default subscription is chosen. |
| `get_subscription` | `subscription_id` | With no argument, resolves the subscription operations act on by default — the fastest way to confirm which estate Cerberus is reading. |
| `list_resource_groups` | `subscription_id` | Name, location, provisioning state. |
| `list_resources` | `subscription_id` | The whole inventory: type, name, resource group, location. |
| `list_ai_accounts` | `subscription_id` | Cognitive Services / AI Services accounts. **Keys are never returned** — see below. |
| `list_model_deployments` | `subscription_id`, `account`, `resource_group` | Deployment name, model, version, sku, capacity. With no `account` it walks every AI account in the subscription. |

```bash
cerberus connectors plugin managed exec azure list_model_deployments
cerberus connectors plugin managed exec azure list_model_deployments --arg account=example-ai-account
```

`subscription_id` is accepted by every operation, so reading a second
subscription does not need a config change.

## Authentication

Two paths, in this order:

**Signed-in Azure CLI user** — the default, and what this connector was verified
against. Nothing to configure: `az login` once and the plugin authenticates as
you, with exactly your role assignments.

**Service principal** — set `tenant_id` and `client_id` as config fields and
supply `client_secret` through the secret provider
(`CERBERUS_AZURE_CLIENT_SECRET`, an `azure: client_secret:` entry in
`~/.cerberus/connector-secrets.yaml`, or `keychain://azure/client_secret`). The
plugin never looks the secret up itself: it declares it in the manifest and
Cerberus resolves it host-side and hands the value over in init config.

A half-configured service principal is refused at load rather than silently
falling back to the CLI credential — reading the estate as an unexpected
identity is worse than not reading it.

`AZURE_SUBSCRIPTION_ID`, `AZURE_TENANT_ID`, `AZURE_CLIENT_ID` and
`AZURE_CLIENT_SECRET` remain fallbacks for running the binary directly, outside
the host, where there is no init config. They do not reach the plugin under the
daemon: the host launches plugins with a fixed environment allow-list carrying
no credentials, by design.

### `az` is probably not on the daemon's PATH

The Azure CLI credential works by shelling out to `az`, and `azidentity` has no
option for where that binary lives — only `PATH`. Under launchd the daemon gets
`PATH=/usr/bin:/bin:/usr/sbin:/sbin`, the plugin subprocess inherits exactly
that, and Homebrew's `az` is at `/opt/homebrew/bin/az`.

So the plugin searches a fallback list and prepends the directory it finds to
its own `PATH`, on every credential build rather than once at boot, and a
failure is reported rather than remembered. That is the shape of the Docker
connector outage recorded in the Cerberus `AGENTS.md`: one cached boot-time
resolution failure, and a connector reporting itself healthy for the daemon's
lifetime.

If `az` genuinely is not installed, the error says so and names the service
principal alternative.

## Keys are never returned

`armcognitiveservices.AccountProperties` carries:

- `APIProperties.QnaAzureSearchEndpointKey` — a search admin key
- `APIProperties.StorageAccountConnectionString` — a storage key, inline
- `APIProperties.EventHubConnectionString` — an Event Hub SAS key
- `MigrationToken`, `Encryption.KeyVaultProperties`, `UserOwnedStorage`

and `armresources.GenericResourceExpanded.Properties` is a bare `any` holding
whatever the resource provider felt like returning. Marshalling either vendor
struct would put all of that into CLI stdout, daemon logs, MCP tool results and
an agent's context window at once.

So `dto.go` maps the vendor types onto a Cerberus-owned allow-list. Cognitive
Services accounts also have *listable* keys — this package never calls
`Accounts.ListKeys`. The account DTO reports the endpoint and
`local_auth_disabled`, so an operator can tell "Entra ID only" from "a key
exists that I am not shown", and never a value.

`dto_test.go` asserts that an account populated with every credential the vendor
type can hold serializes none of them. This is
`docs/adr/0003-connector-response-dtos.md` in practice.

## Not implemented, and why

These are the VM lifecycle operations an Azure connector would normally carry.
They are blocked, and the block is not a code problem — probed 2026-09-17
against `example-subscription`:

| Operation | Blocked by |
|---|---|
| `list_vms`, `get_vm` | `Microsoft.Compute` is **NotRegistered** on the subscription — no VM can exist |
| `start_vm`, `deallocate_vm` | same, plus Virtual Machine Contributor |
| `create_vm`, `delete_vm` | same, plus `Microsoft.Network`, plus Contributor on a resource group |

The reachable subscription is not a compute subscription. Its registered
providers include Storage, KeyVault, CognitiveServices, DocumentDB, Search, Web,
CostManagement and insights — no Compute, no Network; its entire inventory is one
AI Services account and its project. A VM-shaped connector would have nothing to talk to.

**What would unlock them:**

- An admin runs `az provider register -n Microsoft.Compute` and
  `-n Microsoft.Network` at subscription scope. That requires subscription
  Contributor or Owner; there is no narrower built-in role granting only
  registration. Verified failing 2026-09-17: `AuthorizationFailed … does not
  have authorization to perform action 'Microsoft.Compute/register/action'`.
- **Contributor scoped to one resource group** for VM management. Virtual
  Machine Contributor alone is *not* enough to create a VM — it does not cover
  the VNet, NIC and public IP a new VM attaches to. Scoping to a single resource
  group is what makes it least-privilege, not picking a narrower role name.
- The governance question, which is not ours to assume away: this is a shared
  shared subscription. A persistent billable VM in it is a decision
  for whoever owns it, and a sandbox subscription would be the better home.

Where one of these is genuinely wanted, the ask goes to the team that owns the
resource. That is a scope decision, not a permissions workaround.

## Layout

`backend.go` holds the `Backend` interface with the SDK behind it, which is what
makes the dependency swappable and the plugin testable without a network. The
interface returns Cerberus DTOs, so an operation *structurally cannot* return a
vendor struct. Tests run against a fake backend; no test touches Azure.
