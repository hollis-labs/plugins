package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/service"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

const sprite = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8" width="64" height="64"><rect x="1" y="0" width="1" height="1" fill="#ff6b5b"/></svg>`

type fakePreview struct{ calls atomic.Int32 }

func (f *fakePreview) Preview(_ context.Context, r service.PreviewRequest) (*service.Preview, error) {
	f.calls.Add(1)
	if r.Seed == "" {
		return nil, &avatar.FieldError{Field: "seed", Msg: "seed is required"}
	}
	return &service.Preview{Seed: r.Seed, SVG: sprite}, nil
}

func newAPI(t *testing.T, origins ...string) (*API, *fakePreview, string) {
	t.Helper()
	dir := t.TempDir()
	f := &fakePreview{}
	return &API{Store: &store.Store{Dir: dir}, Preview: f, Origins: origins}, f, dir
}

func get(h http.Handler, method, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestServesStoredAvatarsAndNeverGenerates(t *testing.T) {
	a, f, dir := newAPI(t, "https://app.example")
	_ = os.WriteFile(filepath.Join(dir, "agt-1.png"), []byte("PNG64"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "agt-1.svg"), []byte(sprite), 0o644)
	h := a.Handler()

	if w := get(h, "GET", "/avatars/agt-1.png"); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Body.String() != "PNG64" {
		t.Fatalf("png: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Body.String())
	}
	if w := get(h, "GET", "/avatars/agt-1.svg"); w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("svg: %d", w.Code)
	}
	for _, p := range []string{"/avatars/nobody.png", "/avatars/agt-1.gif", "/avatars/..%2f..%2fetc%2fpasswd.png", "/avatars/agt-1@8x.png"} {
		if w := get(h, "GET", p); w.Code != 404 && w.Code != 400 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("serving a file called the engine")
	}
}

func TestCORS(t *testing.T) {
	a, _, _ := newAPI(t, "https://app.example")
	h := a.Handler()
	if w := get(h, "GET", "/healthz", "Origin", "https://app.example"); w.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatal("allowed origin not echoed")
	}
	if w := get(h, "GET", "/healthz", "Origin", "https://evil.example"); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin allowed")
	}
	if w := get(h, "OPTIONS", "/preview", "Origin", "https://app.example"); w.Code != 204 || w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("preflight: %d", w.Code)
	}
	if w := get(h, "POST", "/preview"); w.Code != 405 {
		t.Fatalf("POST: %d", w.Code)
	}
}

type fakeSource struct{}

func (fakeSource) Generate(_ context.Context, r avatar.Request) (service.SVGResult, error) {
	return service.SVGResult{SVG: sprite, RunID: r.RunID()}, nil
}

func TestPreviewPNGFromARealService(t *testing.T) {
	dir := t.TempDir()
	svc := &service.Service{Previewer: fakeSource{}, Registry: &palette.Registry{Path: filepath.Join(dir, "reg.json")}, Store: &store.Store{Dir: dir}}
	h := (&API{Store: svc.Store, Preview: svc}).Handler()
	w := get(h, "GET", "/preview?seed=agt-1&format=png&size=64&palette=violet")
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(w.Body.String(), "\x89PNG") {
		t.Fatalf("png: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if _, err := os.Stat(filepath.Join(dir, "reg.json")); err == nil {
		t.Fatal("a preview took a palette slot")
	}
}

func TestPreviewFormatsCachingAndErrors(t *testing.T) {
	a, f, _ := newAPI(t)
	h := a.Handler()
	if w := get(h, "GET", "/preview?seed=agt-1&fill=40"); w.Code != 200 || w.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(w.Body.String(), "<svg") {
		t.Fatalf("svg: %d %s", w.Code, w.Body.String())
	}
	get(h, "GET", "/preview?seed=agt-1&fill=40")
	if f.calls.Load() != 1 {
		t.Fatalf("identical previews should hit the cache, engine called %d times", f.calls.Load())
	}
	for _, q := range []string{"seed=", "seed=a&fill=x", "seed=a&format=gif", "seed=a&size=7", "seed=a&size=4096"} {
		if w := get(h, "GET", "/preview?"+q); w.Code != 400 {
			t.Errorf("%s: want 400, got %d", q, w.Code)
		}
	}
}
