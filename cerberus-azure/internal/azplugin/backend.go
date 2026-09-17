package azplugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cognitiveservices/armcognitiveservices"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
)

// Config names this plugin declares in its manifest. The host resolves each one
// and hands the value back under the same key in init config, so the manifest
// and the reader must agree — they read from one constant.
const (
	ConfigSubscriptionID = "subscription_id"
	ConfigTenantID       = "tenant_id"
	ConfigClientID       = "client_id"

	// SecretClientSecret is the one secret this plugin declares, and it is
	// optional: with no service principal configured the plugin authenticates
	// as the signed-in Azure CLI user, which is the verified path here.
	SecretClientSecret = "client_secret"
)

// Direct-run fallbacks, for the binary invoked outside the host with no init
// config. They do NOT reach the plugin under the daemon: the host launches
// plugins with an allow-listed environment carrying no credentials by design,
// which is why the secret travels in init config instead.
const (
	SubscriptionEnvVar = "AZURE_SUBSCRIPTION_ID"
	TenantEnvVar       = "AZURE_TENANT_ID"
	ClientIDEnvVar     = "AZURE_CLIENT_ID"
	ClientSecretEnvVar = "AZURE_CLIENT_SECRET"
)

// Credential sources, reported by Probe so an operator can see who Cerberus is
// acting as before wondering why a resource is missing.
const (
	SourceServicePrincipal = "service principal"
	SourceAzureCLI         = "azure cli"
)

// azCLISearchPaths is the fallback search for the `az` binary.
//
// This exists because of a rule in the Cerberus AGENTS.md: launchd hands the
// daemon PATH=/usr/bin:/bin:/usr/sbin:/sbin, the plugin subprocess inherits
// exactly that, and Homebrew's az is at /opt/homebrew/bin/az. Without this the
// plugin works in a terminal and fails under the daemon — the same shape as the
// Docker connector outage that rule was written about.
var azCLISearchPaths = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/usr/bin",
	"/opt/homebrew/opt/azure-cli/bin",
}

// Backend is the seam that keeps the Azure SDK swappable and the plugin
// testable without a network. Everything above it speaks in our own DTOs, so a
// connector operation structurally cannot return a vendor struct.
type Backend interface {
	// ResolveSubscription returns the subscription an operation should act on.
	// An empty request means "the configured one, or the only one there is".
	ResolveSubscription(ctx context.Context, requested string) (Subscription, error)
	ListSubscriptions(ctx context.Context) ([]Subscription, error)
	ListResourceGroups(ctx context.Context, subscriptionID string) ([]ResourceGroup, error)
	ListResources(ctx context.Context, subscriptionID string) ([]Resource, error)
	ListAIAccounts(ctx context.Context, subscriptionID string) ([]AIAccount, error)
	// ListModelDeployments with an empty account walks every AI account in the
	// subscription, which is what makes the zero-argument call useful.
	ListModelDeployments(ctx context.Context, subscriptionID, resourceGroup, account string) ([]ModelDeployment, error)
	// CredentialSource names how the backend authenticates, for diagnostics.
	CredentialSource() string
}

// Credentials carries what the host resolved for us. A zero value is valid and
// means "authenticate as the signed-in Azure CLI user".
type Credentials struct {
	TenantID     string
	ClientID     string
	ClientSecret string
}

// ServicePrincipal reports whether a full service principal was supplied. A
// partial one is a configuration mistake worth naming rather than silently
// falling back from.
func (c Credentials) ServicePrincipal() bool {
	return c.TenantID != "" && c.ClientID != "" && c.ClientSecret != ""
}

func (c Credentials) partial() bool {
	set := 0
	for _, v := range []string{c.TenantID, c.ClientID, c.ClientSecret} {
		if v != "" {
			set++
		}
	}
	return set > 0 && set < 3
}

type sdkBackend struct {
	creds                  Credentials
	configuredSubscription string

	mu sync.Mutex
	// cred and source are memoised on success only. A failure is never cached:
	// the Docker connector cached one boot-time resolution failure for the
	// daemon's lifetime and reported itself healthy the whole time.
	cred     azcore.TokenCredential
	source   string
	resolved string
}

var _ Backend = (*sdkBackend)(nil)

// NewSDKBackend builds a backend against ARM. It performs no network call and
// no credential resolution: both happen on first use, so a plugin loaded
// before the VPN is up still loads.
func NewSDKBackend(subscriptionID string, creds Credentials) (Backend, error) {
	if creds.partial() {
		return nil, fmt.Errorf(
			"incomplete Azure service principal: %s, %s and the %s secret must all be set, or all be unset to authenticate as the signed-in Azure CLI user",
			ConfigTenantID, ConfigClientID, SecretClientSecret)
	}
	return &sdkBackend{creds: creds, configuredSubscription: subscriptionID}, nil
}

func (b *sdkBackend) CredentialSource() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.source != "" {
		return b.source
	}
	if b.creds.ServicePrincipal() {
		return SourceServicePrincipal
	}
	return SourceAzureCLI
}

// credential resolves lazily and memoises only success.
func (b *sdkBackend) credential() (azcore.TokenCredential, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cred != nil {
		return b.cred, nil
	}
	cred, source, err := newCredential(b.creds)
	if err != nil {
		return nil, err
	}
	b.cred, b.source = cred, source
	return cred, nil
}

// newCredential picks the service principal when one was configured and the
// signed-in Azure CLI user otherwise. It never logs or returns the secret.
func newCredential(creds Credentials) (azcore.TokenCredential, string, error) {
	if creds.ServicePrincipal() {
		cred, err := azidentity.NewClientSecretCredential(creds.TenantID, creds.ClientID, creds.ClientSecret, nil)
		if err != nil {
			return nil, "", fmt.Errorf("build Azure service principal credential for client %s in tenant %s: %w", creds.ClientID, creds.TenantID, err)
		}
		return cred, SourceServicePrincipal, nil
	}
	if err := ensureAzureCLIOnPath(); err != nil {
		return nil, "", err
	}
	cred, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		return nil, "", fmt.Errorf("build Azure CLI credential: %w", err)
	}
	return cred, SourceAzureCLI, nil
}

// ensureAzureCLIOnPath makes `az` findable before azidentity shells out to it.
//
// AzureCLICredential has no option for the binary's location, so the only lever
// is PATH. Resolution happens on every credential build rather than once at
// boot, and a failure here is returned rather than remembered.
func ensureAzureCLIOnPath() error {
	if _, err := exec.LookPath("az"); err == nil {
		return nil
	}
	for _, dir := range azCLISearchPaths {
		candidate := filepath.Join(dir, "az")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
			return fmt.Errorf("extend PATH with %s: %w", dir, err)
		}
		return nil
	}
	return fmt.Errorf(
		"the Azure CLI (az) is not on PATH and was not found in %s. "+
			"Under the daemon a plugin inherits launchd's PATH (/usr/bin:/bin:/usr/sbin:/sbin), not your shell's — "+
			"install the CLI, or configure a service principal with %s, %s and the %s secret instead",
		strings.Join(azCLISearchPaths, ", "), ConfigTenantID, ConfigClientID, SecretClientSecret)
}

func (b *sdkBackend) subscriptionsClient() (*armsubscriptions.Client, error) {
	cred, err := b.credential()
	if err != nil {
		return nil, err
	}
	return armsubscriptions.NewClient(cred, nil)
}

func (b *sdkBackend) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	client, err := b.subscriptionsClient()
	if err != nil {
		return nil, b.describeError("list subscriptions", "", err)
	}
	var out []Subscription
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, b.describeError("list subscriptions", "", err)
		}
		out = append(out, SubscriptionsFromSDK(page.Value)...)
	}
	return out, nil
}

// ResolveSubscription answers "which subscription am I acting on" in the order
// an operator would expect: the explicit argument, then the configured default,
// then the one subscription the account can see. It refuses to guess between
// several, because picking one silently is how the wrong estate gets read.
func (b *sdkBackend) ResolveSubscription(ctx context.Context, requested string) (Subscription, error) {
	if requested == "" {
		requested = b.configuredSubscription
	}
	if requested == "" {
		if memo := b.memoisedSubscription(); memo != "" {
			requested = memo
		}
	}

	if requested != "" {
		client, err := b.subscriptionsClient()
		if err != nil {
			return Subscription{}, b.describeError("get subscription "+requested, requested, err)
		}
		resp, err := client.Get(ctx, requested, nil)
		if err != nil {
			return Subscription{}, b.describeError("get subscription "+requested, requested, err)
		}
		sub := SubscriptionFromSDK(&resp.Subscription)
		b.memoiseSubscription(sub.SubscriptionID)
		return sub, nil
	}

	subs, err := b.ListSubscriptions(ctx)
	if err != nil {
		return Subscription{}, err
	}
	switch len(subs) {
	case 0:
		return Subscription{}, fmt.Errorf(
			"no Azure subscription is visible to the %s credential. Sign in with `az login`, or set the %s config field",
			b.CredentialSource(), ConfigSubscriptionID)
	case 1:
		b.memoiseSubscription(subs[0].SubscriptionID)
		return subs[0], nil
	default:
		names := make([]string, 0, len(subs))
		for _, sub := range subs {
			names = append(names, fmt.Sprintf("%s (%s)", sub.DisplayName, sub.SubscriptionID))
		}
		return Subscription{}, fmt.Errorf(
			"%d subscriptions are visible and none was chosen: %s. Set the %s config field, or pass %s to the operation",
			len(subs), strings.Join(names, ", "), ConfigSubscriptionID, ConfigSubscriptionID)
	}
}

func (b *sdkBackend) memoisedSubscription() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resolved
}

func (b *sdkBackend) memoiseSubscription(id string) {
	if id == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.resolved = id
}

func (b *sdkBackend) ListResourceGroups(ctx context.Context, subscriptionID string) ([]ResourceGroup, error) {
	cred, err := b.credential()
	if err != nil {
		return nil, b.describeError("list resource groups", subscriptionID, err)
	}
	client, err := armresources.NewResourceGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, b.describeError("list resource groups", subscriptionID, err)
	}
	var out []ResourceGroup
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, b.describeError("list resource groups", subscriptionID, err)
		}
		out = append(out, ResourceGroupsFromSDK(page.Value)...)
	}
	return out, nil
}

func (b *sdkBackend) ListResources(ctx context.Context, subscriptionID string) ([]Resource, error) {
	cred, err := b.credential()
	if err != nil {
		return nil, b.describeError("list resources", subscriptionID, err)
	}
	client, err := armresources.NewClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, b.describeError("list resources", subscriptionID, err)
	}
	var out []Resource
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, b.describeError("list resources", subscriptionID, err)
		}
		out = append(out, ResourcesFromSDK(page.Value)...)
	}
	return out, nil
}

func (b *sdkBackend) ListAIAccounts(ctx context.Context, subscriptionID string) ([]AIAccount, error) {
	cred, err := b.credential()
	if err != nil {
		return nil, b.describeError("list ai accounts", subscriptionID, err)
	}
	client, err := armcognitiveservices.NewAccountsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, b.describeError("list ai accounts", subscriptionID, err)
	}
	var out []AIAccount
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, b.describeError("list ai accounts", subscriptionID, err)
		}
		out = append(out, AIAccountsFromSDK(page.Value)...)
	}
	return out, nil
}

func (b *sdkBackend) ListModelDeployments(ctx context.Context, subscriptionID, resourceGroup, account string) ([]ModelDeployment, error) {
	targets, err := b.deploymentTargets(ctx, subscriptionID, resourceGroup, account)
	if err != nil {
		return nil, err
	}

	cred, err := b.credential()
	if err != nil {
		return nil, b.describeError("list model deployments", subscriptionID, err)
	}
	client, err := armcognitiveservices.NewDeploymentsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, b.describeError("list model deployments", subscriptionID, err)
	}

	out := make([]ModelDeployment, 0, len(targets))
	for _, target := range targets {
		pager := client.NewListPager(target.ResourceGroup, target.Name, nil)
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				return nil, b.describeError(
					fmt.Sprintf("list model deployments for account %s in resource group %s", target.Name, target.ResourceGroup),
					subscriptionID, err)
			}
			for _, deployment := range page.Value {
				out = append(out, ModelDeploymentFromSDK(deployment, target.Name, target.ResourceGroup))
			}
		}
	}
	return out, nil
}

// deploymentTargets turns the optional account/resource-group arguments into the
// set of accounts to walk. Naming an account without its resource group is the
// common case from the CLI, so that is looked up rather than rejected.
func (b *sdkBackend) deploymentTargets(ctx context.Context, subscriptionID, resourceGroup, account string) ([]AIAccount, error) {
	if account != "" && resourceGroup != "" {
		return []AIAccount{{Name: account, ResourceGroup: resourceGroup}}, nil
	}
	accounts, err := b.ListAIAccounts(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	return selectDeploymentTargets(accounts, subscriptionID, resourceGroup, account)
}

// selectDeploymentTargets is the selection itself, kept free of the network so
// the "which accounts did you mean" behaviour is testable without ARM.
func selectDeploymentTargets(accounts []AIAccount, subscriptionID, resourceGroup, account string) ([]AIAccount, error) {
	if account == "" {
		if resourceGroup == "" {
			return accounts, nil
		}
		filtered := make([]AIAccount, 0, len(accounts))
		for _, candidate := range accounts {
			if strings.EqualFold(candidate.ResourceGroup, resourceGroup) {
				filtered = append(filtered, candidate)
			}
		}
		return filtered, nil
	}

	for _, candidate := range accounts {
		if strings.EqualFold(candidate.Name, account) {
			return []AIAccount{candidate}, nil
		}
	}
	names := make([]string, 0, len(accounts))
	for _, candidate := range accounts {
		names = append(names, candidate.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no AI account named %q in subscription %s, which has no AI accounts at all", account, subscriptionID)
	}
	return nil, fmt.Errorf("no AI account named %q in subscription %s. Accounts present: %s", account, subscriptionID, strings.Join(names, ", "))
}

// describeError names what actually failed. A 403 from ARM means the account is
// authenticated and unauthorised, which is a different problem from a missing
// sign-in, and sending an operator to the wrong one costs an afternoon.
func (b *sdkBackend) describeError(action, subscriptionID string, err error) error {
	if err == nil {
		return nil
	}
	// Name the subscription unless the action already did — "get subscription X
	// on subscription X" tells an operator nothing twice.
	where := action
	if subscriptionID != "" && !strings.Contains(action, subscriptionID) {
		where = fmt.Sprintf("%s (subscription %s)", action, subscriptionID)
	}

	if isCLISignInRequired(err) {
		return fmt.Errorf(
			"%s: the Azure CLI is installed but not signed in. Run `az login` as the user the daemon runs as: %w",
			where, err)
	}

	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case http.StatusForbidden:
			return fmt.Errorf(
				"%s: authorized as the %s credential but forbidden (403, %s). "+
					"This is a role assignment, not a sign-in — ask the subscription owner for Reader at the scope you need: %w",
				where, b.CredentialSource(), respErr.ErrorCode, err)
		case http.StatusUnauthorized:
			return fmt.Errorf(
				"%s: the %s credential was rejected (401). Re-authenticate — `az login`, or check the %s secret has not expired: %w",
				where, b.CredentialSource(), SecretClientSecret, err)
		case http.StatusNotFound:
			// Point at the listing that can actually be run right now: a bad
			// subscription makes every subscription-scoped listing fail too.
			hint := OpListResources
			if strings.HasPrefix(action, "get subscription") {
				hint = OpListSubscriptions
			}
			return fmt.Errorf(
				"%s: not found (404, %s). Check the names — "+
					"`cerberus connectors plugin managed exec azure %s` shows what is actually there: %w",
				where, respErr.ErrorCode, hint, err)
		}
	}
	return fmt.Errorf("%s: %w", where, err)
}

// isCLISignInRequired matches azidentity's "please run az login" family without
// depending on its exact wording for correctness — a false negative here just
// yields the generic message.
func isCLISignInRequired(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "az login") || strings.Contains(text, "please run 'az login'")
}

// resourceGroupFromID pulls the resource group out of an ARM resource id. ARM
// ids are /subscriptions/<id>/resourceGroups/<name>/providers/..., and the
// segment name is case-inconsistent across providers, so match it case-blind.
func resourceGroupFromID(id string) string {
	parts := strings.Split(id, "/")
	for i, part := range parts {
		if strings.EqualFold(part, "resourceGroups") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
