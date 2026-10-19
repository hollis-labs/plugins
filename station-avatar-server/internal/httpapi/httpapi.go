// Package httpapi is the avatar server's read-only HTTP surface, for browsers.
//
// It serves stored avatars as images and unsaved svg previews. It never
// generates, spends or deletes: those stay on the MCP tools behind a token, so
// the endpoints need no credential and a page can call them directly. Cross-
// origin access is limited to the configured origins.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/hollis-labs/plugins/station-avatar-server/internal/avatar"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/hadron"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/palette"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/service"
	"github.com/hollis-labs/plugins/station-avatar-server/internal/store"
)

// Previewer draws unsaved avatars. *service.Service is the real one.
type Previewer interface {
	Preview(ctx context.Context, r service.PreviewRequest) (*service.Preview, error)
}

const (
	maxPreviewPx     = 1024
	defaultPreviewPx = 256
	maxInFlight      = 4
	cacheEntries     = 256
)

// API is the handler.
type API struct {
	Store   *store.Store
	Preview Previewer
	// Origins are the browser origins allowed cross-origin; "*" allows any (the
	// data is public and read-only). Empty allows none.
	Origins []string

	once  sync.Once
	slots chan struct{}
	mu    sync.Mutex
	cache map[string]*service.Preview
}

func (a *API) init() {
	a.once.Do(func() {
		a.slots = make(chan struct{}, maxInFlight)
		a.cache = map[string]*service.Preview{}
	})
}

// Handler returns the routes.
func (a *API) Handler() http.Handler {
	a.init()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/avatars/", a.avatar)
	mux.HandleFunc("/preview", a.preview)
	return a.cors(mux)
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			w.Header().Add("Vary", "Origin")
			for _, allowed := range a.Origins {
				if allowed == "*" || allowed == o {
					w.Header().Set("Access-Control-Allow-Origin", allowed)
					w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
					w.Header().Set("Access-Control-Max-Age", "600")
					break
				}
			}
		}
		switch r.Method {
		case http.MethodOptions:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet, http.MethodHead:
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD, OPTIONS")
			fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "this surface is read-only")
		}
	})
}

// avatar serves GET /avatars/<urn>.svg, .png or @8x.png from the store. An agent
// with no avatar is a 404: nothing is generated here.
func (a *API) avatar(w http.ResponseWriter, r *http.Request) {
	file := strings.TrimPrefix(r.URL.Path, "/avatars/")
	var stem, kind string
	switch {
	case strings.HasSuffix(file, "@8x.png"):
		stem, kind = strings.TrimSuffix(file, "@8x.png"), "png8x"
	case strings.HasSuffix(file, ".png"):
		stem, kind = strings.TrimSuffix(file, ".png"), "png"
	case strings.HasSuffix(file, ".svg"):
		stem, kind = strings.TrimSuffix(file, ".svg"), "svg"
	default:
		fail(w, http.StatusNotFound, "not_found", "ask for <urn>.svg, <urn>.png or <urn>@8x.png")
		return
	}
	name, err := store.SafeName(stem)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_urn", "not a valid avatar name")
		return
	}
	p := a.Store.Paths(name)
	path, ctype := p.PNG, "image/png"
	switch kind {
	case "png8x":
		path = p.PNG8x
	case "svg":
		path, ctype = p.SVG, "image/svg+xml"
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "no such avatar")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		fail(w, http.StatusNotFound, "not_found", "no such avatar")
		return
	}
	w.Header().Set("Content-Type", ctype)
	// A forced regenerate replaces the file, so let caches revalidate.
	w.Header().Set("Cache-Control", "public, max-age=60, must-revalidate")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// preview serves GET /preview?seed=&fill=&palette=|accent=&bg=&format=svg|png&size=.
func (a *API) preview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := service.PreviewRequest{Seed: q.Get("seed"), Palette: q.Get("palette"), Accent: hash(q.Get("accent")), BG: hash(q.Get("bg"))}
	if v := q.Get("fill"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_argument", "fill must be an integer")
			return
		}
		req.Fill = &n
	}
	format := q.Get("format")
	if format == "" {
		format = "svg"
	}
	px := defaultPreviewPx
	if v := q.Get("size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 8 || n > maxPreviewPx || n%8 != 0 {
			fail(w, http.StatusBadRequest, "invalid_argument", "size must be a multiple of 8 from 8 to "+strconv.Itoa(maxPreviewPx))
			return
		}
		px = n
	}
	if format != "svg" && format != "png" {
		fail(w, http.StatusBadRequest, "invalid_argument", "format must be svg or png")
		return
	}

	prev, err := a.draw(r.Context(), req)
	if err != nil {
		a.drawError(w, err)
		return
	}
	// Previews are a pure function of the query, so a browser may keep them.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if format == "png" {
		b, err := prev.PNG(px)
		if err != nil {
			fail(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(prev.SVG))
}

// hash lets a query carry a colour without percent-encoding the '#'.
func hash(s string) string {
	if len(s) == 6 {
		return "#" + s
	}
	return s
}

func (a *API) draw(ctx context.Context, req service.PreviewRequest) (*service.Preview, error) {
	fill := -1
	if req.Fill != nil {
		fill = *req.Fill
	}
	key := strings.Join([]string{req.Seed, req.Palette, req.Accent, req.BG, strconv.Itoa(fill)}, "\x00")
	a.mu.Lock()
	hit := a.cache[key]
	a.mu.Unlock()
	if hit != nil {
		return hit, nil
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	prev, err := a.Preview.Preview(ctx, req)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if len(a.cache) >= cacheEntries {
		a.cache = map[string]*service.Preview{}
	}
	a.cache[key] = prev
	a.mu.Unlock()
	return prev, nil
}

func (a *API) drawError(w http.ResponseWriter, err error) {
	var fe *avatar.FieldError
	switch {
	case errors.As(err, &fe):
		fail(w, http.StatusBadRequest, "invalid_argument", fe.Msg)
	case errors.Is(err, hadron.ErrUnavailable), errors.Is(err, hadron.ErrTimeout), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		w.Header().Set("Retry-After", "5")
		fail(w, http.StatusServiceUnavailable, "hadron_unavailable", "the svg engine is not reachable; retry shortly")
	case errors.Is(err, palette.ErrCorrupt), errors.Is(err, palette.ErrLocked):
		fail(w, http.StatusServiceUnavailable, "registry_unavailable", err.Error())
	default:
		fail(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
