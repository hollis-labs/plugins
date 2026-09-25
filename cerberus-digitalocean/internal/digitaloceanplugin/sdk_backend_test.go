package digitaloceanplugin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/digitalocean/godo"
)

// The compiled-in connector read one page of 100 droplets and dropped the
// rest. list_droplets follows DigitalOcean's page links to the end.
func TestSDKBackendListDropletsReadsEveryPage(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/droplets" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("per_page"); got != strconv.Itoa(listPageSize) {
			t.Errorf("per_page = %q, want %d", got, listPageSize)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		links := ""
		if page < 3 {
			links = fmt.Sprintf(`,"links":{"pages":{"next":"%s/v2/droplets?page=%d&per_page=%d","last":"%s/v2/droplets?page=3&per_page=%d"}}`,
				server.URL, page+1, listPageSize, server.URL, listPageSize)
		}
		_, _ = fmt.Fprintf(w, `{"droplets":[{"id":%d,"name":"d%d","status":"active"}]%s}`, page, page, links)
	}))
	defer server.Close()

	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	droplets, err := newSDKBackendWithClient(client).ListDroplets(context.Background())
	if err != nil {
		t.Fatalf("ListDroplets: %v", err)
	}
	if len(droplets) != 3 || droplets[0].ID != 1 || droplets[2].ID != 3 {
		t.Fatalf("droplets = %+v, want one from each of three pages", droplets)
	}
}

// The recorded shape of a real read: the boundary keeps the public IPv4 and
// the slugs, exactly as the compiled-in connector's read test asserted.
func TestSDKBackendGetDropletMapsTheRecordedShape(t *testing.T) {
	const droplet = `{"id":42,"name":"api","status":"active","created_at":"2026-09-01T12:00:00Z","region":{"slug":"nyc3"},"size":{"slug":"s-1vcpu-1gb"},"image":{"slug":"ubuntu-24-04-x64"},"networks":{"v4":[{"type":"private","ip_address":"10.0.0.1"},{"type":"public","ip_address":"192.0.2.8"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/droplets/42" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"droplet":%s}`, droplet)
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	got, err := newSDKBackendWithClient(client).GetDroplet(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 42 || got.IPv4 != "192.0.2.8" || got.Region != "nyc3" || got.Size != "s-1vcpu-1gb" || got.Image != "ubuntu-24-04-x64" || got.Status != "active" || got.CreatedAt.IsZero() {
		t.Fatalf("mapping = %+v", got)
	}
}

// A server whose page links never end must not hold list_droplets open
// forever: the walk stops at maxListPages and says so.
func TestSDKBackendListDropletsStopsOnEndlessLinks(t *testing.T) {
	requests := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"droplets":[{"id":1,"name":"d","status":"active"}],"links":{"pages":{"next":"%s/v2/droplets?page=999","last":"%s/v2/droplets?page=9999"}}}`, server.URL, server.URL)
	}))
	defer server.Close()
	client := godo.NewClient(server.Client())
	client.BaseURL, _ = url.Parse(server.URL + "/")
	_, err := newSDKBackendWithClient(client).ListDroplets(context.Background())
	if err == nil || requests != maxListPages {
		t.Fatalf("err = %v after %d requests, want a refusal after %d", err, requests, maxListPages)
	}
}
