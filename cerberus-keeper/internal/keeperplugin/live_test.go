package keeperplugin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveResolve resolves one operator-named reference against real Keeper,
// through New(): the production transport and client, exactly as the host
// will run it. It reads one record and writes nothing.
//
// It runs only when both are set, and scripts/live-check.sh is the way to run
// it:
//
//	CERBERUS_KEEPER_KSM_CONFIG  a bound Secrets Manager configuration
//	KEEPER_LIVE_REF             a keeper:// reference to a test record
//
// The value is never printed: the test reports its length alone.
func TestLiveResolve(t *testing.T) {
	ref := strings.TrimSpace(os.Getenv("KEEPER_LIVE_REF"))
	if ref == "" || os.Getenv(envVar(SecretKSMConfig)) == "" {
		t.Skip("live check: set KEEPER_LIVE_REF and " + envVar(SecretKSMConfig) + " (see scripts/live-check.sh)")
	}
	p := New()
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status := p.status(); !status.Configured {
		t.Fatalf("configuration not usable: %s", status.Message)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	value, err := p.resolve(ctx, ref)
	if err != nil {
		t.Fatalf("resolve failed: %s", p.errorText(err, ref))
	}
	t.Logf("resolved the reference: %d characters", len(value))

	// The same reference again reaches Keeper again: nothing was kept.
	if _, err := p.resolve(ctx, ref); err != nil {
		t.Fatalf("second resolve failed: %s", p.errorText(err, ref))
	}
	t.Log("second resolve reached Keeper and succeeded")
}
