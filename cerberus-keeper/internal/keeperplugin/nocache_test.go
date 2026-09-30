package keeperplugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hollis-labs/plugin-sdk/subprocess"
	ksm "github.com/keeper-security/secrets-manager-go/core"
	klog "github.com/keeper-security/secrets-manager-go/core/logger"
)

// resolvedSentinel is the value the fake Keeper serves.
const resolvedSentinel = "kR4vT9wQ2zLm8xNp" //nolint:gosec // a test sentinel, not a credential

// fakeKeeper is a Keeper Secrets Manager endpoint, faked at the transport the
// real SDK client sends through. It answers get_secret with one record shared
// directly to the application, encrypted the way Keeper encrypts it, until
// fail is set; after that every request fails at the network, as an
// unreachable Keeper does.
type fakeKeeper struct {
	t        *testing.T
	ctx      **ksm.Context
	appKey   []byte
	uid      string
	mu       sync.Mutex
	fail     bool
	requests int
}

func (f *fakeKeeper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.fail {
		return nil, errors.New("dial tcp: connect: network is unreachable")
	}
	if !strings.HasSuffix(req.URL.Path, "/get_secret") {
		f.t.Errorf("unexpected Keeper call %s", req.URL.Path)
	}
	record, _ := json.Marshal(map[string]any{
		"title":  "Deploy database",
		"type":   "login",
		"fields": []map[string]any{{"type": "password", "value": []string{resolvedSentinel}}},
	})
	data, err := ksm.EncryptAesGcm(record, f.appKey)
	if err != nil {
		f.t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"records": []map[string]any{{"recordUid": f.uid, "data": ksm.BytesToBase64(data), "revision": 1}},
	})
	// The SDK has just put this request's transmission key in the context.
	encrypted, err := ksm.EncryptAesGcm(body, (*f.ctx).TransmissionKey.Key)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewReader(encrypted)), Header: http.Header{}, Request: req}, nil
}

// boundConfig is a bound Secrets Manager configuration for the fake.
func boundConfig(t *testing.T, appKey []byte) map[string]string {
	t.Helper()
	der, err := ksm.GeneratePrivateKeyDer()
	if err != nil {
		t.Fatal(err)
	}
	clientID, err := ksm.GetRandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"hostname":   "keeper.invalid",
		"clientId":   ksm.BytesToBase64(clientID),
		"privateKey": ksm.BytesToBase64(der),
		"appKey":     ksm.BytesToBase64(appKey),
	}
}

// newFakeKeeper builds the real SDK client through newManagerWithContext, the
// constructor production uses, with the fake as its transport.
func newFakeKeeper(t *testing.T) (*fakeKeeper, *ksm.SecretsManager) {
	t.Helper()
	appKey, err := ksm.GetRandomBytes(32)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeKeeper{t: t, appKey: appKey, uid: ksm.GenerateUid()}
	keeperContext := &ksm.Context{Transport: fake}
	fake.ctx = &keeperContext
	sm, err := newManagerWithContext(boundConfig(t, appKey), &keeperContext)
	if err != nil {
		t.Fatal(err)
	}
	return fake, sm
}

// The rule this plugin exists to keep: when Keeper cannot be reached, a
// resolve fails. It never answers with the value it resolved last time.
//
// This runs the real SDK client end to end: the fake answers the first
// get_secret, then goes unreachable, and the second resolve of the same
// reference must be a credential_missing failure that does not carry the
// value.
func TestAFailingTransportNeverServesAPriorValue(t *testing.T) {
	fake, sm := newFakeKeeper(t)
	p := newPlugin(func(map[string]string) (vault, error) { return sm, nil })
	t.Setenv(envVar(SecretKSMConfig), `{"hostname":"keeper.invalid","clientId":"c2VudGluZWwtY2xpZW50","privateKey":"cHJpdmF0ZS1rZXktc2VudGluZWw=","appKey":"YXBwLWtleS1zZW50aW5lbA=="}`)
	if _, err := p.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	ref := "keeper://" + fake.uid + "/field/password"

	first := resolveOnce(t, p, ref)
	if first.Action != "message" || first.Content != resolvedSentinel {
		t.Fatalf("first resolve = %+v, want the value", first)
	}

	fake.mu.Lock()
	fake.fail = true
	before := fake.requests
	fake.mu.Unlock()

	second := resolveOnce(t, p, ref)
	if second.Action != "error" {
		t.Fatalf("a resolve with Keeper unreachable succeeded: %+v", second)
	}
	if strings.Contains(second.Content, resolvedSentinel) {
		t.Fatalf("the failure carries the prior value: %s", second.Content)
	}
	if !strings.Contains(second.Content, `"credential_missing"`) {
		t.Fatalf("the failure is not credential_missing: %s", second.Content)
	}
	if fake.requests == before {
		t.Fatal("the second resolve never reached Keeper; something answered it from memory")
	}
}

// The test above has teeth: the same client with the SDK's cache installed
// does serve the prior value when Keeper is unreachable. If this ever stops
// holding, the SDK changed and the test above needs a new way to catch a
// cache.
func TestTheSDKCacheWouldServeAPriorValue(t *testing.T) {
	fake, sm := newFakeKeeper(t)
	sm.SetCache(&memoryCache{})
	ref := "keeper://" + fake.uid + "/field/password"
	if got, err := sm.GetNotationResults(ref); err != nil || len(got) != 1 || got[0] != resolvedSentinel {
		t.Fatalf("first read = %v, %v", got, err)
	}
	fake.mu.Lock()
	fake.fail = true
	fake.mu.Unlock()
	got, err := sm.GetNotationResults(ref)
	if err != nil || len(got) != 1 || got[0] != resolvedSentinel {
		t.Fatalf("with a cache the SDK no longer serves the prior value (%v, %v); rework the no-cache test", got, err)
	}
}

// The client the plugin builds has no cache installed. SetCache is the only
// way in and the field is unexported, so this reads it by reflection.
func TestTheClientHasNoCache(t *testing.T) {
	_, sm := newFakeKeeper(t)
	cache := reflect.ValueOf(sm).Elem().FieldByName("cache")
	if !cache.IsValid() {
		t.Fatal("the SDK client has no cache field any more; re-check how it caches before trusting TestAFailingTransportNeverServesAPriorValue")
	}
	if !cache.IsNil() {
		t.Fatal("the client has a cache installed")
	}
}

// Nothing in the plugin's own code reaches for the SDK's cache.
func TestNoCodeInstallsTheSDKCache(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file) //nolint:gosec // this package's own source
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{".SetCache(", "NewFileCache(", "ksm.ICache", "ksm_cache"} {
			if bytes.Contains(src, []byte(banned)) {
				t.Errorf("%s references %s; this plugin must never cache Keeper responses", file, banned)
			}
		}
	}
}

// KSM_SKIP_VERIFY cannot turn certificate checks off, and KSM_CONFIG cannot
// replace the configuration the host delivered.
func TestEnvironmentCannotWeakenTheClient(t *testing.T) {
	t.Setenv("KSM_SKIP_VERIFY", "true")
	t.Setenv("KSM_CONFIG", `{"hostname":"elsewhere.invalid","clientId":"b3RoZXI=","privateKey":"b3RoZXI=","appKey":"b3RoZXI="}`)
	appKey, _ := ksm.GetRandomBytes(32)
	config := boundConfig(t, appKey)
	sm, err := newManager(config, newTransport())
	if err != nil {
		t.Fatal(err)
	}
	if !sm.VerifySslCerts {
		t.Fatal("KSM_SKIP_VERIFY turned certificate verification off")
	}
	if got := sm.Config.Get(ksm.KEY_CLIENT_ID); got != config["clientId"] {
		t.Fatal("KSM_CONFIG replaced the delivered configuration")
	}
	if tr, ok := newTransport().(*http.Transport); !ok || tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("the transport does not verify certificates")
	}
}

// The SDK logs to stdout by default, which is the protocol stream. The plugin
// moves it before anything can log.
func TestKeeperLogStaysOffTheProtocolStream(t *testing.T) {
	klog.SetOutput(os.Stdout)
	_ = New()
	if klog.Writer() == os.Stdout {
		t.Fatal("the Keeper SDK still logs to stdout, the plugin protocol stream")
	}
}

func resolveOnce(t *testing.T, p *Plugin, ref string) subprocess.CommandResult {
	t.Helper()
	args, _ := json.Marshal(resolveArgs{Ref: ref})
	res, err := p.Command(context.Background(), subprocess.CommandRequest{Name: ResolveCommand, Args: string(args)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

type memoryCache struct{ data []byte }

func (c *memoryCache) SaveCachedValue(data []byte) error {
	c.data = append([]byte(nil), data...)
	return nil
}
func (c *memoryCache) GetCachedValue() ([]byte, error) { return c.data, nil }
func (c *memoryCache) Purge() error                    { c.data = nil; return nil }
