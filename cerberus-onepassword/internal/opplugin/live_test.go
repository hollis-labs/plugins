package opplugin

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveResolve resolves one operator-named reference against real
// 1Password through New(): the production transport, fence and client,
// exactly as the host will run them. It reads one field and writes nothing.
//
// It runs only when both are set, and scripts/live-check.sh is the way to run
// it:
//
//	CERBERUS_ONEPASSWORD_SERVICE_ACCOUNT_TOKEN  a service account token
//	OP_LIVE_REF                                 an op:// reference to a test item
//
// The value is never printed: the test reports its length, and the timings
// that are the baseline for the plugin's cold start.
//
// It ends with the half of "never serves a prior value" no fake can reach:
// after a real success, 1Password is made unreachable, and the same
// reference, through the same signed-in client, must fail.
func TestLiveResolve(t *testing.T) {
	ref := strings.TrimSpace(os.Getenv("OP_LIVE_REF"))
	if ref == "" || os.Getenv(envVar(SecretServiceAccountToken)) == "" {
		t.Skip("live check: set OP_LIVE_REF and " + envVar(SecretServiceAccountToken) + " (see scripts/live-check.sh)")
	}
	saved := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = saved })
	p := New()
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := p.status(); !status.Configured {
		t.Fatalf("token not usable: %s", status.Message)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	value, err := p.resolve(ctx, ref)
	if err != nil {
		t.Fatalf("first resolve failed: %s", p.errorText(err, ref))
	}
	t.Logf("cold: WASM compile, sign-in and first resolve took %v; value is %d characters", time.Since(start).Round(time.Millisecond), len(value))

	start = time.Now()
	if _, err := p.resolve(ctx, ref); err != nil {
		t.Fatalf("second resolve failed: %s", p.errorText(err, ref))
	}
	t.Logf("warm: a resolve on the signed-in client took %v", time.Since(start).Round(time.Millisecond))

	http.DefaultClient = &http.Client{Transport: &refusingTransport{}, Timeout: 5 * time.Second}
	got, err := p.resolve(ctx, ref)
	if err == nil {
		t.Fatalf("with 1Password unreachable the resolve succeeded (%d characters): the SDK answered from memory", len(got))
	}
	t.Logf("unreachable: the resolve failed as it must: %s", p.errorText(err, ref))
}
