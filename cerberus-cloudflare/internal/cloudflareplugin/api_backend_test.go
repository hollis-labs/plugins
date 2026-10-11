package cloudflareplugin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeCloudflare is a fake API. Nothing here touches a real account; every id
// and name is a placeholder.
type fakeCloudflare struct {
	mu       sync.Mutex
	requests []recorded
	handler  func(w http.ResponseWriter, r *http.Request, body []byte)
}

type recorded struct {
	Method, Path, Auth, Jurisdiction string
	Body                             string
}

func newFakeCloudflare(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*fakeCloudflare, *apiBackend) {
	t.Helper()
	f := &fakeCloudflare{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recorded{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("cf-r2-jurisdiction"), string(body)})
		f.mu.Unlock()
		f.handler(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return f, newAPIBackend(srv.URL, testToken)
}

// testToken is a placeholder, never a real credential.
const testToken = "test-token-not-a-secret-0001"

// ok writes a Cloudflare success envelope around result.
func ok(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"success":true,"errors":[],"result":`+result+`}`)
}

// fail writes a Cloudflare failure envelope.
func fail(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(message)
	_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":`+itoa(code)+`,"message":`+string(b)+`}],"result":null}`)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (f *fakeCloudflare) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// verify_token sends the token as a bearer credential, reads the token's
// policies when Cloudflare allows it, and maps them onto the DTO without the
// fields that are not named.
func TestVerifyTokenMapsStatusAndPermissionGroups(t *testing.T) {
	fake, api := newFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case "/user/tokens/verify":
			ok(w, `{"id":"tok1","status":"active","expires_on":"2027-01-01T00:00:00Z","not_before":"2026-01-01T00:00:00Z"}`)
		case "/user/tokens/tok1":
			ok(w, `{"id":"tok1","name":"my secret token name","status":"active","issued_on":"x",
"condition":{"request_ip":{"in":["198.51.100.0/24"]}},
"policies":[{"id":"p","effect":"allow","resources":{"com.cloudflare.api.account.zone.zone1":"*"},
"permission_groups":[{"id":"g1","name":"DNS Write"},{"id":"g2","name":"Zone Read"}]}]}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	got, err := api.VerifyToken(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if fake.requests[0].Auth != "Bearer "+testToken {
		t.Errorf("auth = %q", fake.requests[0].Auth)
	}
	if got.ID != "tok1" || got.Status != "active" || got.Scope != "user" || got.ExpiresOn == "" || !got.PoliciesAvailable {
		t.Fatalf("status = %+v", got)
	}
	if len(got.Policies) != 1 || strings.Join(got.Policies[0].PermissionGroups, ",") != "DNS Write,Zone Read" ||
		got.Policies[0].Resources[0] != "com.cloudflare.api.account.zone.zone1 = *" {
		t.Fatalf("policies = %+v", got.Policies)
	}
	data, _ := json.Marshal(got)
	for _, leaked := range []string{"my secret token name", "198.51.100", "g1", "issued_on"} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("DTO leaked %q: %s", leaked, data)
		}
	}
}

func TestVerifyTokenUsesTheAccountEndpointForAnAccountToken(t *testing.T) {
	fake, api := newFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if strings.HasSuffix(r.URL.Path, "/verify") {
			ok(w, `{"id":"tok1","status":"active"}`)
			return
		}
		fail(w, 403, 9109, "Unauthorized to access requested resource")
	})
	got, err := api.VerifyToken(t.Context(), "acct1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != "account" || got.PoliciesAvailable || got.Note == "" || got.Policies == nil {
		t.Fatalf("status = %+v", got)
	}
	if want := "GET /accounts/acct1/tokens/verify"; fake.paths()[0] != want {
		t.Errorf("first request = %q, want %q", fake.paths()[0], want)
	}
}

func TestVerifyTokenSurfacesARejectedToken(t *testing.T) {
	_, api := newFakeCloudflare(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fail(w, 401, 1000, "Invalid API Token")
	})
	_, err := api.VerifyToken(t.Context(), "")
	if err == nil || errorCode(err) != "credential_missing" || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, code %q", err, errorCode(err))
	}
}

// A 200 whose envelope says success is false is a failure, not an empty result.
func TestEnvelopeFailureOnA200IsAnError(t *testing.T) {
	_, api := newFakeCloudflare(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":7003,"message":"no route"}],"result":null}`)
	})
	if _, err := api.VerifyToken(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("err = %v", err)
	}
}

func TestBucketsMapToDTOsAndCarryTheJurisdiction(t *testing.T) {
	fake, api := newFakeCloudflare(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == "GET":
			ok(w, `{"buckets":[{"name":"example-backups","creation_date":"2026-10-01T00:00:00Z","location":"WEUR","storage_class":"Standard","jurisdiction":"eu","somethingNew":"x"}]}`)
		case r.Method == "POST":
			ok(w, `{"name":"example-backups","creation_date":"2026-10-01T00:00:00Z","storage_class":"Standard"}`)
		default:
			ok(w, `{}`)
		}
	})
	list, err := api.ListBuckets(t.Context(), "acct1", "eu")
	if err != nil || len(list) != 1 || list[0].Location != "WEUR" || list[0].Jurisdiction != "eu" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if fake.requests[0].Jurisdiction != "eu" || !strings.Contains(fake.requests[0].Path, "/accounts/acct1/r2/buckets") {
		t.Errorf("request = %+v", fake.requests[0])
	}
	if data, _ := json.Marshal(list); strings.Contains(string(data), "somethingNew") {
		t.Errorf("DTO leaked an unnamed field: %s", data)
	}

	if _, err := api.CreateBucket(t.Context(), "acct1", BucketRequest{Name: "example-backups", LocationHint: "weur", StorageClass: "Standard"}); err != nil {
		t.Fatal(err)
	}
	if body := fake.requests[1].Body; !strings.Contains(body, `"locationHint":"weur"`) || !strings.Contains(body, `"storageClass":"Standard"`) || fake.requests[1].Jurisdiction != "" {
		t.Errorf("create request = %+v", fake.requests[1])
	}
	if err := api.DeleteBucket(t.Context(), "acct1", "example-backups", ""); err != nil {
		t.Fatal(err)
	}
	if got := fake.paths()[2]; got != "DELETE /accounts/acct1/r2/buckets/example-backups" {
		t.Errorf("delete request = %q", got)
	}
}

func TestDeleteOfANonEmptyBucketSurfacesCloudflaresRefusal(t *testing.T) {
	_, api := newFakeCloudflare(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		fail(w, 409, 10008, "The bucket you tried to delete is not empty.")
	})
	err := api.DeleteBucket(t.Context(), "acct1", "example-backups", "")
	if err == nil || !strings.Contains(err.Error(), "not empty") || !strings.Contains(err.Error(), "409") {
		t.Fatalf("err = %v", err)
	}
}
