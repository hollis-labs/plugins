package azplugin

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

// A half-configured service principal is a mistake worth naming. Falling back
// to the CLI credential silently would read the estate as the wrong identity.
func TestNewSDKBackendRejectsPartialServicePrincipal(t *testing.T) {
	_, err := NewSDKBackend("", Credentials{TenantID: "tenant", ClientID: "client"})
	if err == nil {
		t.Fatal("a service principal missing its secret was accepted")
	}
	for _, want := range []string{ConfigTenantID, ConfigClientID, SecretClientSecret} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %q: %v", want, err)
		}
	}
}

// No credential at all is the verified path: authenticate as the signed-in
// Azure CLI user.
func TestNewSDKBackendAcceptsNoCredential(t *testing.T) {
	backend, err := NewSDKBackend("", Credentials{})
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	if got := backend.CredentialSource(); got != SourceAzureCLI {
		t.Fatalf("CredentialSource = %q, want %q", got, SourceAzureCLI)
	}
}

func TestCredentialSourceNamesServicePrincipal(t *testing.T) {
	backend, err := NewSDKBackend("", Credentials{TenantID: "t", ClientID: "c", ClientSecret: "s"})
	if err != nil {
		t.Fatalf("NewSDKBackend: %v", err)
	}
	if got := backend.CredentialSource(); got != SourceServicePrincipal {
		t.Fatalf("CredentialSource = %q, want %q", got, SourceServicePrincipal)
	}
}

func TestResourceGroupFromID(t *testing.T) {
	cases := map[string]string{
		"/subscriptions/sub-1/resourceGroups/Azure-rg-example-ai/providers/Microsoft.CognitiveServices/accounts/x": "Azure-rg-example-ai",
		// ARM is inconsistent about the segment's case across providers.
		"/subscriptions/sub-1/resourcegroups/lower-case-rg/providers/Microsoft.Storage/storageAccounts/y": "lower-case-rg",
		"/subscriptions/sub-1": "",
		"":                     "",
		// A truncated id must not index past the end.
		"/subscriptions/sub-1/resourceGroups": "",
	}
	for id, want := range cases {
		if got := resourceGroupFromID(id); got != want {
			t.Fatalf("resourceGroupFromID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSelectDeploymentTargets(t *testing.T) {
	accounts := []AIAccount{
		{Name: "example-ai-account", ResourceGroup: "Azure-rg-example-ai"},
		{Name: "other-account", ResourceGroup: "rg-example-dev-eastus-01"},
	}

	t.Run("no filter walks every account", func(t *testing.T) {
		got, err := selectDeploymentTargets(accounts, "sub-1", "", "")
		if err != nil || len(got) != 2 {
			t.Fatalf("got %v, err %v", got, err)
		}
	})

	t.Run("account name is matched case-blind and finds its group", func(t *testing.T) {
		got, err := selectDeploymentTargets(accounts, "sub-1", "", "example-ai-account")
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if len(got) != 1 || got[0].ResourceGroup != "Azure-rg-example-ai" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("resource group alone filters", func(t *testing.T) {
		got, err := selectDeploymentTargets(accounts, "sub-1", "rg-example-dev-eastus-01", "")
		if err != nil {
			t.Fatalf("err %v", err)
		}
		if len(got) != 1 || got[0].Name != "other-account" {
			t.Fatalf("got %v", got)
		}
	})

	// A typo should say what is actually there rather than returning nothing.
	t.Run("unknown account names the accounts that exist", func(t *testing.T) {
		_, err := selectDeploymentTargets(accounts, "sub-1", "", "typo")
		if err == nil {
			t.Fatal("unknown account was accepted")
		}
		if !strings.Contains(err.Error(), "example-ai-account") {
			t.Fatalf("error does not name the accounts present: %v", err)
		}
	})

	t.Run("unknown account in an empty subscription says so", func(t *testing.T) {
		_, err := selectDeploymentTargets(nil, "sub-1", "", "typo")
		if err == nil || !strings.Contains(err.Error(), "no AI accounts at all") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ensureAzureCLIOnPath exists for one reason: under the daemon a plugin
// inherits launchd's PATH, which does not include Homebrew. Prove it repairs
// PATH rather than failing the way the Docker connector once did.
func TestEnsureAzureCLIOnPathFindsABinaryOffPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "az"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake az: %v", err)
	}

	original := azCLISearchPaths
	azCLISearchPaths = []string{dir}
	t.Cleanup(func() { azCLISearchPaths = original })
	t.Setenv("PATH", t.TempDir())

	if err := ensureAzureCLIOnPath(); err != nil {
		t.Fatalf("ensureAzureCLIOnPath: %v", err)
	}
	if !strings.HasPrefix(os.Getenv("PATH"), dir) {
		t.Fatalf("PATH was not extended with %s: %s", dir, os.Getenv("PATH"))
	}
}

// When az is genuinely absent the error must name the daemon PATH problem and
// the service principal alternative, because those are the two ways out.
func TestEnsureAzureCLIOnPathNamesTheRecovery(t *testing.T) {
	original := azCLISearchPaths
	azCLISearchPaths = []string{t.TempDir()}
	t.Cleanup(func() { azCLISearchPaths = original })
	t.Setenv("PATH", t.TempDir())

	err := ensureAzureCLIOnPath()
	if err == nil {
		t.Fatal("a missing az was not reported")
	}
	for _, want := range []string{"launchd", SecretClientSecret, ConfigTenantID} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error does not name %q: %v", want, err)
		}
	}
}

// A 403 means authenticated and unauthorised — a role assignment problem.
// Reporting it as a sign-in problem sends an operator to `az login`, which
// will succeed and change nothing.
func TestDescribeErrorSeparatesAuthorizationFromSignIn(t *testing.T) {
	backend := &sdkBackend{}

	forbidden := backend.describeError("list resources", "sub-1",
		&azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailed"})
	if !strings.Contains(forbidden.Error(), "role assignment") {
		t.Fatalf("403 not described as an authorization problem: %v", forbidden)
	}
	if strings.Contains(forbidden.Error(), "az login") {
		t.Fatalf("403 sends the operator to az login, which will not help: %v", forbidden)
	}

	signIn := backend.describeError("list resources", "sub-1",
		errors.New("AzureCLICredential: please run 'az login' to set up an account"))
	if !strings.Contains(signIn.Error(), "az login") {
		t.Fatalf("a sign-in failure does not name az login: %v", signIn)
	}

	notFound := backend.describeError("list model deployments", "sub-1",
		&azcore.ResponseError{StatusCode: 404, ErrorCode: "ResourceNotFound"})
	if !strings.Contains(notFound.Error(), OpListResources) {
		t.Fatalf("404 does not name the operation that shows what exists: %v", notFound)
	}
}

// A bad subscription makes every subscription-scoped listing fail too, so
// pointing at list_resources there sends the operator in a circle.
func TestDescribeErrorPointsAtAListingThatCanBeRun(t *testing.T) {
	backend := &sdkBackend{}
	err := backend.describeError("get subscription sub-typo", "sub-typo",
		&azcore.ResponseError{StatusCode: 404, ErrorCode: "SubscriptionNotFound"})
	if !strings.Contains(err.Error(), OpListSubscriptions) {
		t.Fatalf("a bad subscription does not point at %s: %v", OpListSubscriptions, err)
	}
	if strings.Contains(err.Error(), OpListResources) {
		t.Fatalf("a bad subscription points at an operation that needs a good one: %v", err)
	}
}

// The subscription must be named once, not twice.
func TestDescribeErrorDoesNotRepeatTheSubscription(t *testing.T) {
	backend := &sdkBackend{}
	err := backend.describeError("get subscription sub-1", "sub-1", errors.New("boom"))
	if strings.Count(err.Error(), "sub-1") != 1 {
		t.Fatalf("subscription named %d times: %v", strings.Count(err.Error(), "sub-1"), err)
	}
}

// Every error must name the subscription it was talking to. "Not found" without
// a subscription is the same unhelpful message the Docker connector used to
// give for the wrong host.
func TestDescribeErrorNamesTheSubscription(t *testing.T) {
	backend := &sdkBackend{}
	err := backend.describeError("list resources", "sub-1", errors.New("boom"))
	if !strings.Contains(err.Error(), "sub-1") {
		t.Fatalf("error does not name the subscription: %v", err)
	}
}

// The host runs redact.Text over operator-facing error text on every path, and
// it has eaten its own recovery guidance four times. These are the two patterns
// from internal/redact/redact.go that do the eating; the instructions this
// plugin emits must survive both.
var (
	hostBearerPattern     = regexp.MustCompile(`(?i)\bBearer[ \t]+([a-z0-9._~+/=-]+)`)
	hostAssignmentPattern = regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|passcode|private[_-]?key|credentials?|authorization|cookie)[a-z0-9_.-]*["']?\s*(?:=>|=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&<>]+)`)
)

func TestRecoveryInstructionsSurviveHostRedaction(t *testing.T) {
	backend := &sdkBackend{}

	azMissing := func() string {
		original := azCLISearchPaths
		azCLISearchPaths = []string{t.TempDir()}
		defer func() { azCLISearchPaths = original }()
		t.Setenv("PATH", t.TempDir())
		return ensureAzureCLIOnPath().Error()
	}()

	partial, err := NewSDKBackend("", Credentials{TenantID: "t"})
	if err == nil {
		t.Fatalf("expected a partial credential error, got backend %v", partial)
	}

	messages := []string{
		azMissing,
		err.Error(),
		backend.describeError("list resources", "sub-1", &azcore.ResponseError{StatusCode: 401}).Error(),
		backend.describeError("list resources", "sub-1", &azcore.ResponseError{StatusCode: 403, ErrorCode: "AuthorizationFailed"}).Error(),
		backend.describeError("list resources", "sub-1", errors.New("please run 'az login'")).Error(),
	}

	for _, message := range messages {
		if hostBearerPattern.MatchString(message) {
			t.Fatalf("message would be garbled by the host's bearer redaction:\n%s", message)
		}
		if match := hostAssignmentPattern.FindString(message); match != "" {
			t.Fatalf("message would be garbled by the host's assignment redaction at %q:\n%s", match, message)
		}
	}
}
