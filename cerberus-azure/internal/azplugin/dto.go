package azplugin

import (
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cognitiveservices/armcognitiveservices"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// The types in this file are the security boundary required by
// docs/adr/0003-connector-response-dtos.md in the Cerberus repo. They are an
// allow-list: a field the vendor adds in a minor release is not emitted unless
// someone adds it here on purpose.
//
// The Azure case is not hypothetical. armcognitiveservices.AccountProperties
// carries:
//
//   - APIProperties.QnaAzureSearchEndpointKey        — a search admin key
//   - APIProperties.StorageAccountConnectionString   — a storage key, inline
//   - APIProperties.EventHubConnectionString         — an Event Hub SAS key
//   - MigrationToken                                 — a resource migration token
//   - Encryption.KeyVaultProperties                  — key vault key identifiers
//   - UserOwnedStorage                               — customer storage resource ids
//
// and armresources.GenericResourceExpanded.Properties is a bare `any` holding
// whatever the resource provider felt like returning. Marshalling either vendor
// struct straight out would put that into CLI stdout, daemon logs, MCP tool
// results and an agent's context window at once.
//
// Cognitive Services accounts also have *listable* keys. We never call
// Accounts.ListKeys: the account DTO reports the endpoint and whether local-key
// auth is even enabled, never a key value. That is the `probe-*` convention —
// names, never values — so this output is safe to paste into a document.

// Subscription is the Cerberus view of an Azure subscription.
type Subscription struct {
	SubscriptionID string `json:"subscription_id"`
	DisplayName    string `json:"display_name,omitempty"`
	State          string `json:"state,omitempty"`
	TenantID       string `json:"tenant_id,omitempty"`
}

// ResourceGroup is the Cerberus view of a resource group.
type ResourceGroup struct {
	Name              string `json:"name"`
	Location          string `json:"location,omitempty"`
	ProvisioningState string `json:"provisioning_state,omitempty"`
}

// Resource is the Cerberus view of one entry in a subscription's inventory.
// It deliberately has no `properties` field: the vendor type carries that as a
// bare `any` whose contents are whatever the resource provider returns.
type Resource struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	ResourceGroup string `json:"resource_group,omitempty"`
	Location      string `json:"location,omitempty"`
	Kind          string `json:"kind,omitempty"`
	SKU           string `json:"sku,omitempty"`
	ID            string `json:"id,omitempty"`
}

// AIAccount is the Cerberus view of a Cognitive Services / AI Services account.
//
// LocalAuthDisabled and Endpoint are the two fields an operator actually needs:
// where to call it, and whether calling it requires a key at all. No key value
// reaches this struct, because nothing in this package ever asks for one.
type AIAccount struct {
	Name              string `json:"name"`
	ResourceGroup     string `json:"resource_group,omitempty"`
	Kind              string `json:"kind,omitempty"`
	Location          string `json:"location,omitempty"`
	SKU               string `json:"sku,omitempty"`
	Endpoint          string `json:"endpoint,omitempty"`
	ProvisioningState string `json:"provisioning_state,omitempty"`

	// LocalAuthDisabled reports whether key-based auth is turned off, so an
	// operator can tell "Entra ID only" from "a key exists that I am not
	// showing you". It is the shape of the credential, never the credential.
	LocalAuthDisabled bool `json:"local_auth_disabled"`

	// AssociatedProjects names the AI Foundry projects hanging off the account.
	AssociatedProjects []string `json:"associated_projects,omitempty"`
}

// ModelDeployment is the Cerberus view of a model deployment: what can actually
// be called, at what version, under what quota.
type ModelDeployment struct {
	Name              string `json:"name"`
	Account           string `json:"account"`
	ResourceGroup     string `json:"resource_group,omitempty"`
	Model             string `json:"model,omitempty"`
	ModelVersion      string `json:"model_version,omitempty"`
	ModelFormat       string `json:"model_format,omitempty"`
	SKU               string `json:"sku,omitempty"`
	Capacity          int32  `json:"capacity,omitempty"`
	ProvisioningState string `json:"provisioning_state,omitempty"`
	UpgradeOption     string `json:"upgrade_option,omitempty"`
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// SubscriptionFromSDK maps one vendor subscription onto our allow-list.
func SubscriptionFromSDK(s *armsubscriptions.Subscription) Subscription {
	if s == nil {
		return Subscription{}
	}
	out := Subscription{
		SubscriptionID: deref(s.SubscriptionID),
		DisplayName:    deref(s.DisplayName),
		TenantID:       deref(s.TenantID),
	}
	if s.State != nil {
		out.State = string(*s.State)
	}
	return out
}

func SubscriptionsFromSDK(in []*armsubscriptions.Subscription) []Subscription {
	out := make([]Subscription, 0, len(in))
	for _, s := range in {
		out = append(out, SubscriptionFromSDK(s))
	}
	return out
}

// ResourceGroupFromSDK maps one vendor resource group onto our allow-list.
func ResourceGroupFromSDK(g *armresources.ResourceGroup) ResourceGroup {
	if g == nil {
		return ResourceGroup{}
	}
	out := ResourceGroup{
		Name:     deref(g.Name),
		Location: deref(g.Location),
	}
	if g.Properties != nil {
		out.ProvisioningState = deref(g.Properties.ProvisioningState)
	}
	return out
}

func ResourceGroupsFromSDK(in []*armresources.ResourceGroup) []ResourceGroup {
	out := make([]ResourceGroup, 0, len(in))
	for _, g := range in {
		out = append(out, ResourceGroupFromSDK(g))
	}
	return out
}

// ResourceFromSDK maps one inventory entry onto our allow-list. This function is
// a security boundary; read it as one. In particular it does not touch
// `Properties`, which is an untyped provider blob.
func ResourceFromSDK(r *armresources.GenericResourceExpanded) Resource {
	if r == nil {
		return Resource{}
	}
	out := Resource{
		Name:     deref(r.Name),
		Type:     deref(r.Type),
		Location: deref(r.Location),
		Kind:     deref(r.Kind),
		ID:       deref(r.ID),
	}
	if r.SKU != nil {
		out.SKU = deref(r.SKU.Name)
	}
	out.ResourceGroup = resourceGroupFromID(out.ID)
	return out
}

func ResourcesFromSDK(in []*armresources.GenericResourceExpanded) []Resource {
	out := make([]Resource, 0, len(in))
	for _, r := range in {
		out = append(out, ResourceFromSDK(r))
	}
	return out
}

// AIAccountFromSDK maps one Cognitive Services account onto our allow-list.
// This function is the security boundary for the credential-bearing vendor type
// described at the top of this file; read it as one.
func AIAccountFromSDK(a *armcognitiveservices.Account) AIAccount {
	if a == nil {
		return AIAccount{}
	}
	out := AIAccount{
		Name:          deref(a.Name),
		Kind:          deref(a.Kind),
		Location:      deref(a.Location),
		ResourceGroup: resourceGroupFromID(deref(a.ID)),
	}
	if a.SKU != nil {
		out.SKU = deref(a.SKU.Name)
	}
	if p := a.Properties; p != nil {
		out.Endpoint = deref(p.Endpoint)
		out.LocalAuthDisabled = deref(p.DisableLocalAuth)
		if p.ProvisioningState != nil {
			out.ProvisioningState = string(*p.ProvisioningState)
		}
		for _, project := range p.AssociatedProjects {
			if name := deref(project); name != "" {
				out.AssociatedProjects = append(out.AssociatedProjects, name)
			}
		}
	}
	return out
}

func AIAccountsFromSDK(in []*armcognitiveservices.Account) []AIAccount {
	out := make([]AIAccount, 0, len(in))
	for _, a := range in {
		out = append(out, AIAccountFromSDK(a))
	}
	return out
}

// ModelDeploymentFromSDK maps one deployment onto our allow-list. The account
// and resource group are carried in rather than read from the vendor struct:
// a deployment's own ID is the only place they appear, and the caller already
// knows them.
func ModelDeploymentFromSDK(d *armcognitiveservices.Deployment, account, resourceGroup string) ModelDeployment {
	if d == nil {
		return ModelDeployment{}
	}
	out := ModelDeployment{
		Name:          deref(d.Name),
		Account:       account,
		ResourceGroup: resourceGroup,
	}
	if d.SKU != nil {
		out.SKU = deref(d.SKU.Name)
		out.Capacity = deref(d.SKU.Capacity)
	}
	if p := d.Properties; p != nil {
		if p.Model != nil {
			out.Model = deref(p.Model.Name)
			out.ModelVersion = deref(p.Model.Version)
			out.ModelFormat = deref(p.Model.Format)
		}
		if p.ProvisioningState != nil {
			out.ProvisioningState = string(*p.ProvisioningState)
		}
		if p.VersionUpgradeOption != nil {
			out.UpgradeOption = string(*p.VersionUpgradeOption)
		}
		if out.Capacity == 0 {
			out.Capacity = deref(p.CurrentCapacity)
		}
	}
	return out
}
