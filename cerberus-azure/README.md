# azure

Cerberus connector for Azure Resource Manager — read-only inventory, and the
model deployments an AI Services account actually serves.

> **Pre-release — v0.1.0.** Six read-only operations. No provisioning, write or
> lifecycle operations are implemented; cost reporting, Key Vault and Resource
> Graph were each probed and are documented as blocked with what would unlock
> them. No outside users, no compatibility guarantees, no support channel.
> Built in the open: interfaces and behavior can change without notice.

## Scope: read and probe only

This connector assumes you do not own the estate it reads. A subscription is
usually administered by someone else, and a tool that helps you operate it is a
different thing from a tool that manages it. So every operation here is
read-only: each declares effect `read`, none takes `--ack`, and a test asserts that
rather than leaving it to review.

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
cerberus connectors exec azure list_model_deployments
cerberus connectors exec azure list_model_deployments --arg account=my-ai-account
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
They are absent because on the subscriptions this was developed against they
could not have worked, and building them unverified would have shipped six
operations nobody had ever seen succeed.

| Operation | Needs |
|---|---|
| `list_vms`, `get_vm` | `Microsoft.Compute` registered on the subscription — until it is, no VM can exist to list |
| `start_vm`, `deallocate_vm` | the above, plus Virtual Machine Contributor |
| `create_vm`, `delete_vm` | the above, plus `Microsoft.Network`, plus Contributor on a resource group |

Check what your own subscription actually serves before assuming a VM operation
is missing rather than impossible — `az provider list --query "[?registrationState=='Registered'].namespace"`.
A subscription provisioned for AI or data services commonly has no Compute or
Network registered at all, and a VM-shaped connector would have nothing to talk
to.

**Three things worth knowing if you want to add them:**

- **Registering a provider is an admin action.** `az provider register -n
  Microsoft.Compute` is scoped to the subscription and needs Contributor or
  Owner. There is no narrower built-in role that grants only registration, so
  this is a request to whoever owns the subscription, not something a
  least-privilege identity can arrange for itself.
- **Virtual Machine Contributor alone cannot create a VM.** It does not cover
  the VNet, NIC and public IP a new VM attaches to. Least privilege here comes
  from scoping Contributor to a single resource group, not from picking a
  narrower-sounding role.
- **A persistent billable VM is a governance decision, not a technical one.**
  If the subscription is shared, whoever owns it should be the one to agree to
  it, and a dedicated sandbox subscription is usually the better home.

Where a write operation is genuinely wanted, the ask goes to whoever owns the
resource. That is a scope decision, not a permissions workaround.

## Layout

`backend.go` holds the `Backend` interface with the SDK behind it, which is what
makes the dependency swappable and the plugin testable without a network. The
interface returns Cerberus DTOs, so an operation *structurally cannot* return a
vendor struct. Tests run against a fake backend; no test touches Azure.
