package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echo-ssr/echo/internal/loader"
	"github.com/echo-ssr/echo/internal/router"
)

// ---------------------------------------------------------------------------
// TTFB — page serving
// ---------------------------------------------------------------------------
func BenchmarkHandlePage_NoLoader(b *testing.B) {
	shell := "<html><body><div id=\"root\"></div></body></html>"
	cp := compiledPage{
		route: router.Route{Pattern: "/about", BundleKey: "about"},
		shell: shell,
	}
	s := &Server{
		goLoaders: map[string]LoaderFunc{},
		jsLoaders: map[string]*loader.Loader{},
	}
	req := httptest.NewRequest(http.MethodGet, "/about", nil)

	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		s.handlePage(rec, req, cp, http.StatusOK)
	}
}

func BenchmarkHandlePage_WithGoLoader(b *testing.B) {
	shell := "<html><body><div id=\"root\"></div></body></html>"
	cp := compiledPage{
		route: router.Route{Pattern: "/about", BundleKey: "about"},
		shell: shell,
	}
	s := &Server{
		goLoaders: map[string]LoaderFunc{
			"/about": func(_ *http.Request) (any, error) {
				return map[string]string{"title": "About", "body": "Hello"}, nil
			},
		},
		jsLoaders: map[string]*loader.Loader{},
	}
	req := httptest.NewRequest(http.MethodGet, "/about", nil)

	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		s.handlePage(rec, req, cp, http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// Data injection
// ---------------------------------------------------------------------------
func BenchmarkInjectLoaderData(b *testing.B) {
	shell := "<html><head></head><body><div id=\"root\"></div></body></html>"
	data := json.RawMessage(`{"message":"hello","count":42,"nested":{"a":1,"b":true}}`)

	b.ResetTimer()
	for b.Loop() {
		injectLoaderData(shell, data)
	}
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------
func BenchmarkGzipMiddleware_Compressing(b *testing.B) {
	payload := []byte("<html><body><div id=\"root\"></div><script type=\"module\" src=\"/bundle.js\"></script></body></html>")
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(payload)
	})
	h := gzipMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

func BenchmarkGzipMiddleware_Passthrough(b *testing.B) {
	payload := []byte("<html><body></body></html>")
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	h := gzipMiddleware(inner)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	for b.Loop() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------
func BenchmarkExtractPathParams(b *testing.B) {
	req := httptest.NewRequest(http.MethodGet, "/blog/hello-world", nil)
	req.SetPathValue("slug", "hello-world")

	b.ResetTimer()
	for b.Loop() {
		extractPathParams("/blog/{slug}", req)
	}
}

func BenchmarkAffectedBundleKeys(b *testing.B) {
	routes := make([]router.Route, 20)
	inputs := make(map[string]map[string]struct{}, 20)
	for i := range routes {
		key := router.Route{
			Pattern:   "/page",
			BundleKey: string(rune('a' + i)),
			FilePath:  "/app/pages/" + string(rune('a'+i)) + ".tsx",
		}
		routes[i] = key
		inputs[key.BundleKey] = map[string]struct{}{
			key.FilePath:          {},
			"/app/shared/nav.tsx": {},
		}
	}
	changed := map[string]struct{}{"/app/shared/nav.tsx": {}}

	b.ResetTimer()
	for b.Loop() {
		affectedBundleKeys(routes, inputs, changed)
	}
}
