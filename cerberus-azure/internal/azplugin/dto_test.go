package azplugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cognitiveservices/armcognitiveservices"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// Sentinel values are distinctive so a leak is unambiguous in the serialized
// output rather than a substring of something innocent.
const (
	sentinelSearchKey     = "SENTINEL-QNA-SEARCH-KEY-b3a1"
	sentinelStorageConn   = "SENTINEL-STORAGE-CONNECTION-9f2c"
	sentinelEventHubConn  = "SENTINEL-EVENTHUB-CONNECTION-4d8e"
	sentinelMigration     = "SENTINEL-MIGRATION-TOKEN-7c5b"
	sentinelKeyVaultURI   = "SENTINEL-KEYVAULT-URI-1a6f"
	sentinelKeyName       = "SENTINEL-KEYVAULT-KEYNAME-2e9d"
	sentinelStorageRes    = "SENTINEL-USER-OWNED-STORAGE-5b7c"
	sentinelProviderBlob  = "SENTINEL-PROVIDER-PROPERTY-8a3d"
	sentinelAADClientID   = "SENTINEL-AAD-CLIENT-ID-6f4b"
	sentinelSuperUser     = "SENTINEL-SUPERUSER-0c2e"
	sentinelWebsiteName   = "SENTINEL-WEBSITE-NAME-3d9a"
	sentinelInternalIDVal = "SENTINEL-INTERNAL-ID-7e1c"
)

func strptr(s string) *string { return &s }

// fullyPopulatedAccount carries every credential-bearing field
// armcognitiveservices.Account can hold. The natural implementation —
// marshalling the vendor struct — emits all of them.
func fullyPopulatedAccount() *armcognitiveservices.Account {
	kind := "AIServices"
	location := "eastus2"
	skuName := "S0"
	endpoint := "https://example-ai-account.cognitiveservices.azure.com/"
	provisioning := armcognitiveservices.ProvisioningStateSucceeded
	disableLocalAuth := true
	keySource := armcognitiveservices.KeySourceMicrosoftKeyVault

	return &armcognitiveservices.Account{
		ID:       strptr("/subscriptions/sub-1/resourceGroups/rg-example-ai/providers/Microsoft.CognitiveServices/accounts/Example-AI-Account"),
		Name:     strptr("Example-AI-Account"),
		Kind:     &kind,
		Location: &location,
		SKU:      &armcognitiveservices.SKU{Name: &skuName},
		Properties: &armcognitiveservices.AccountProperties{
			Endpoint:           &endpoint,
			ProvisioningState:  &provisioning,
			DisableLocalAuth:   &disableLocalAuth,
			AssociatedProjects: []*string{strptr("Example-AI-Account")},
			MigrationToken:     strptr(sentinelMigration),
			InternalID:         strptr(sentinelInternalIDVal),
			APIProperties: &armcognitiveservices.APIProperties{
				QnaAzureSearchEndpointKey:      strptr(sentinelSearchKey),
				StorageAccountConnectionString: strptr(sentinelStorageConn),
				EventHubConnectionString:       strptr(sentinelEventHubConn),
				AADClientID:                    strptr(sentinelAADClientID),
				SuperUser:                      strptr(sentinelSuperUser),
				WebsiteName:                    strptr(sentinelWebsiteName),
			},
			Encryption: &armcognitiveservices.Encryption{
				KeySource: &keySource,
				KeyVaultProperties: &armcognitiveservices.KeyVaultProperties{
					KeyVaultURI: strptr(sentinelKeyVaultURI),
					KeyName:     strptr(sentinelKeyName),
				},
			},
			UserOwnedStorage: []*armcognitiveservices.UserOwnedStorage{
				{ResourceID: strptr(sentinelStorageRes)},
			},
		},
	}
}

// This is the test ADR 0003 requires: an account carrying every credential the
// vendor type can hold must not serialize any of them.
func TestAIAccountDTODoesNotSerializeCredentials(t *testing.T) {
	encoded, err := json.Marshal(AIAccountFromSDK(fullyPopulatedAccount()))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(encoded)

	for _, secret := range []string{
		sentinelSearchKey, sentinelStorageConn, sentinelEventHubConn,
		sentinelMigration, sentinelKeyVaultURI, sentinelKeyName,
		sentinelStorageRes, sentinelAADClientID, sentinelSuperUser,
		sentinelWebsiteName, sentinelInternalIDVal,
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("serialized account leaked %q:\n%s", secret, got)
		}
	}

	// The vendor's JSON field names must not appear either — their presence
	// would mean the SDK struct is being embedded rather than mapped.
	for _, field := range []string{
		"apiProperties", "qnaAzureSearchEndpointKey", "storageAccountConnectionString",
		"eventHubConnectionString", "migrationToken", "encryption",
		"keyVaultProperties", "userOwnedStorage", "internalId",
	} {
		if strings.Contains(got, field) {
			t.Fatalf("serialized account carries vendor field %q:\n%s", field, got)
		}
	}
}

// The shape of the credential configuration is still reported: an operator must
// be able to tell "Entra ID only" from "a key exists that I am not shown".
func TestAIAccountDTOReportsCredentialShape(t *testing.T) {
	dto := AIAccountFromSDK(fullyPopulatedAccount())
	if !dto.LocalAuthDisabled {
		t.Fatal("LocalAuthDisabled = false, want true — the account has key auth turned off")
	}
	if dto.Endpoint == "" {
		t.Fatal("Endpoint is empty; an operator needs somewhere to call")
	}
	if dto.ResourceGroup != "rg-example-ai" {
		t.Fatalf("ResourceGroup = %q, want it derived from the ARM id", dto.ResourceGroup)
	}
	if dto.SKU != "S0" || dto.Kind != "AIServices" {
		t.Fatalf("SKU/Kind = %q/%q, want S0/AIServices", dto.SKU, dto.Kind)
	}
	if len(dto.AssociatedProjects) != 1 || dto.AssociatedProjects[0] != "Example-AI-Account" {
		t.Fatalf("AssociatedProjects = %v", dto.AssociatedProjects)
	}
}

// GenericResourceExpanded.Properties is a bare `any` holding whatever the
// resource provider returned. The inventory DTO must not carry it through.
func TestResourceDTODropsUntypedProviderProperties(t *testing.T) {
	skuName := "S0"
	kind := "AIServices"
	location := "eastus2"
	vendor := &armresources.GenericResourceExpanded{
		ID:       strptr("/subscriptions/sub-1/resourceGroups/rg-example-ai/providers/Microsoft.CognitiveServices/accounts/Example-AI-Account"),
		Name:     strptr("Example-AI-Account"),
		Type:     strptr("Microsoft.CognitiveServices/accounts"),
		Kind:     &kind,
		Location: &location,
		SKU:      &armresources.SKU{Name: &skuName},
		Properties: map[string]any{
			"connectionString": sentinelProviderBlob,
		},
	}

	encoded, err := json.Marshal(ResourceFromSDK(vendor))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), sentinelProviderBlob) {
		t.Fatalf("serialized resource leaked the provider property blob:\n%s", encoded)
	}

	dto := ResourceFromSDK(vendor)
	if dto.ResourceGroup != "rg-example-ai" {
		t.Fatalf("ResourceGroup = %q, want it derived from the ARM id", dto.ResourceGroup)
	}
	if dto.Type != "Microsoft.CognitiveServices/accounts" {
		t.Fatalf("Type = %q", dto.Type)
	}
}

func TestSubscriptionFromSDK(t *testing.T) {
	state := armsubscriptions.SubscriptionStateEnabled
	dto := SubscriptionFromSDK(&armsubscriptions.Subscription{
		SubscriptionID: strptr("00000000-0000-0000-0000-000000000000"),
		DisplayName:    strptr("example-subscription"),
		TenantID:       strptr("423946e4-28c0-4deb-904c-a4a4b174fb3f"),
		State:          &state,
	})
	if dto.SubscriptionID != "00000000-0000-0000-0000-000000000000" || dto.State != "Enabled" {
		t.Fatalf("SubscriptionFromSDK = %+v", dto)
	}
}

func TestResourceGroupFromSDKCarriesProvisioningState(t *testing.T) {
	dto := ResourceGroupFromSDK(&armresources.ResourceGroup{
		Name:       strptr("rg-example-ai"),
		Location:   strptr("eastus2"),
		Properties: &armresources.ResourceGroupProperties{ProvisioningState: strptr("Succeeded")},
	})
	if dto.Name != "rg-example-ai" || dto.Location != "eastus2" || dto.ProvisioningState != "Succeeded" {
		t.Fatalf("ResourceGroupFromSDK = %+v", dto)
	}
}

// The deployment DTO is the operationally interesting one: it answers "what can
// we call, at what version" without opening the portal.
func TestModelDeploymentFromSDK(t *testing.T) {
	provisioning := armcognitiveservices.DeploymentProvisioningStateSucceeded
	upgrade := armcognitiveservices.DeploymentModelVersionUpgradeOptionOnceNewDefaultVersionAvailable
	capacity := int32(5000)
	skuName := "GlobalStandard"

	dto := ModelDeploymentFromSDK(&armcognitiveservices.Deployment{
		Name: strptr("claude-sonnet-5"),
		SKU:  &armcognitiveservices.SKU{Name: &skuName, Capacity: &capacity},
		Properties: &armcognitiveservices.DeploymentProperties{
			Model: &armcognitiveservices.DeploymentModel{
				Name:    strptr("claude-sonnet-5"),
				Version: strptr("2"),
				Format:  strptr("Anthropic"),
			},
			ProvisioningState:    &provisioning,
			VersionUpgradeOption: &upgrade,
		},
	}, "Example-AI-Account", "rg-example-ai")

	want := ModelDeployment{
		Name:              "claude-sonnet-5",
		Account:           "Example-AI-Account",
		ResourceGroup:     "rg-example-ai",
		Model:             "claude-sonnet-5",
		ModelVersion:      "2",
		ModelFormat:       "Anthropic",
		SKU:               "GlobalStandard",
		Capacity:          5000,
		ProvisioningState: "Succeeded",
		UpgradeOption:     "OnceNewDefaultVersionAvailable",
	}
	if dto != want {
		t.Fatalf("ModelDeploymentFromSDK =\n%+v\nwant\n%+v", dto, want)
	}
}

// A nil vendor pointer anywhere in a page must not panic a read-only listing.
func TestMappersTolerateNilInput(t *testing.T) {
	if got := AIAccountFromSDK(nil); got.Name != "" || len(got.AssociatedProjects) != 0 {
		t.Fatalf("AIAccountFromSDK(nil) = %+v", got)
	}
	if got := ResourceFromSDK(nil); got != (Resource{}) {
		t.Fatalf("ResourceFromSDK(nil) = %+v", got)
	}
	if got := ResourceGroupFromSDK(nil); got != (ResourceGroup{}) {
		t.Fatalf("ResourceGroupFromSDK(nil) = %+v", got)
	}
	if got := SubscriptionFromSDK(nil); got != (Subscription{}) {
		t.Fatalf("SubscriptionFromSDK(nil) = %+v", got)
	}
	if got := ModelDeploymentFromSDK(nil, "a", "b"); got != (ModelDeployment{}) {
		t.Fatalf("ModelDeploymentFromSDK(nil) = %+v", got)
	}
}
