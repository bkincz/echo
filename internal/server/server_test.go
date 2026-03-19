package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/echo-ssr/echo/internal/loader"
	"github.com/echo-ssr/echo/internal/router"
)

// ---------------------------------------------------------------------------
// titleFromPattern
// ---------------------------------------------------------------------------
func TestTitleFromPattern(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		want    string
	}{
		{"/", "Home"},
		{"/about", "About"},
		{"/blog", "Blog"},
		{"/blog/{id}", "Blog"},
		{"/docs/{slug...}", "Docs"},
		{"/a/b/c", "C"},
		{"/{id}", "Echo"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()
			got := titleFromPattern(tc.pattern)
			if got != tc.want {
				t.Errorf("titleFromPattern(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// routesChanged
// ---------------------------------------------------------------------------
func TestRoutesChanged(t *testing.T) {
	t.Parallel()

	r := func(pattern, key, fp string) router.Route {
		return router.Route{Pattern: pattern, BundleKey: key, FilePath: fp}
	}

	t.Run("identical", func(t *testing.T) {
		t.Parallel()
		prev := []router.Route{r("/about", "about", "/app/pages/about.tsx")}
		next := []router.Route{r("/about", "about", "/app/pages/about.tsx")}
		if routesChanged(prev, next) {
			t.Error("expected false, got true")
		}
	})

	t.Run("different length", func(t *testing.T) {
		t.Parallel()
		prev := []router.Route{r("/a", "a", "/a.tsx")}
		next := []router.Route{r("/a", "a", "/a.tsx"), r("/b", "b", "/b.tsx")}
		if !routesChanged(prev, next) {
			t.Error("expected true, got false")
		}
	})

	t.Run("different pattern", func(t *testing.T) {
		t.Parallel()
		prev := []router.Route{r("/about", "about", "/app/pages/about.tsx")}
		next := []router.Route{r("/contact", "about", "/app/pages/about.tsx")}
		if !routesChanged(prev, next) {
			t.Error("expected true, got false")
		}
	})

	t.Run("different filepath", func(t *testing.T) {
		t.Parallel()
		prev := []router.Route{r("/about", "about", "/app/pages/about.tsx")}
		next := []router.Route{r("/about", "about", "/app/pages/about.jsx")}
		if !routesChanged(prev, next) {
			t.Error("expected true, got false")
		}
	})

	t.Run("empty slices", func(t *testing.T) {
		t.Parallel()
		if routesChanged(nil, nil) {
			t.Error("expected false, got true")
		}
	})
}

// ---------------------------------------------------------------------------
// affectedBundleKeys
// ---------------------------------------------------------------------------
func TestAffectedBundleKeys(t *testing.T) {
	t.Parallel()

	routes := []router.Route{
		{Pattern: "/about", BundleKey: "about", FilePath: "/app/pages/about.tsx"},
		{Pattern: "/blog/{id}", BundleKey: "blog/[id]", FilePath: "/app/pages/blog/[id].tsx"},
	}
	inputs := map[string]map[string]struct{}{
		"about": {
			"/app/pages/about.tsx":    {},
			"/app/components/nav.tsx": {},
		},
		"blog/[id]": {
			"/app/pages/blog/[id].tsx": {},
		},
	}

	t.Run("direct page file change", func(t *testing.T) {
		t.Parallel()
		changed := map[string]struct{}{"/app/pages/about.tsx": {}}
		got := affectedBundleKeys(routes, inputs, changed)
		if _, ok := got["about"]; !ok {
			t.Error("expected 'about' to be affected")
		}
		if _, ok := got["blog/[id]"]; ok {
			t.Error("expected 'blog/[id]' not to be affected")
		}
	})

	t.Run("shared dependency change", func(t *testing.T) {
		t.Parallel()
		changed := map[string]struct{}{"/app/components/nav.tsx": {}}
		got := affectedBundleKeys(routes, inputs, changed)
		if _, ok := got["about"]; !ok {
			t.Error("expected 'about' to be affected via dependency")
		}
		if _, ok := got["blog/[id]"]; ok {
			t.Error("expected 'blog/[id]' not to be affected")
		}
	})

	t.Run("meta.json sidecar change", func(t *testing.T) {
		t.Parallel()
		changed := map[string]struct{}{"/app/pages/about.meta.json": {}}
		got := affectedBundleKeys(routes, inputs, changed)
		if _, ok := got["about"]; !ok {
			t.Error("expected 'about' to be affected via .meta.json sidecar")
		}
	})

	t.Run("unrelated file change", func(t *testing.T) {
		t.Parallel()
		changed := map[string]struct{}{"/app/public/logo.png": {}}
		got := affectedBundleKeys(routes, inputs, changed)
		if len(got) != 0 {
			t.Errorf("expected no affected keys, got %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// readPageMeta
// ---------------------------------------------------------------------------
func TestReadPageMeta(t *testing.T) {
	t.Parallel()

	t.Run("no sidecar falls back to titleFromPattern", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pagePath := filepath.Join(dir, "about.tsx")
		title, desc := readPageMeta(pagePath, "/about")
		if title != "About" {
			t.Errorf("title = %q, want %q", title, "About")
		}
		if desc != "" {
			t.Errorf("description = %q, want empty", desc)
		}
	})

	t.Run("sidecar with title and description", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pagePath := filepath.Join(dir, "about.tsx")
		meta := map[string]string{"title": "About Us", "description": "Learn more"}
		data, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dir, "about.meta.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		title, desc := readPageMeta(pagePath, "/about")
		if title != "About Us" {
			t.Errorf("title = %q, want %q", title, "About Us")
		}
		if desc != "Learn more" {
			t.Errorf("description = %q, want %q", desc, "Learn more")
		}
	})

	t.Run("sidecar with empty title falls back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pagePath := filepath.Join(dir, "contact.tsx")
		meta := map[string]string{"title": "", "description": "Get in touch"}
		data, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dir, "contact.meta.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
		title, desc := readPageMeta(pagePath, "/contact")
		if title != "Contact" {
			t.Errorf("title = %q, want %q", title, "Contact")
		}
		if desc != "Get in touch" {
			t.Errorf("description = %q, want %q", desc, "Get in touch")
		}
	})

	t.Run("malformed sidecar falls back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pagePath := filepath.Join(dir, "index.tsx")
		if err := os.WriteFile(filepath.Join(dir, "index.meta.json"), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		title, _ := readPageMeta(pagePath, "/")
		if title != "Home" {
			t.Errorf("title = %q, want %q", title, "Home")
		}
	})
}

// ---------------------------------------------------------------------------
// bundleID
// ---------------------------------------------------------------------------
func TestBundleID(t *testing.T) {
	t.Parallel()

	devServer := &Server{devMode: true}
	prodServer := &Server{devMode: false}

	t.Run("dev: stable across different JS content", func(t *testing.T) {
		t.Parallel()
		id1 := devServer.bundleID("about", "js-content-v1", "")
		id2 := devServer.bundleID("about", "js-content-v2", "")
		if id1 != id2 {
			t.Errorf("dev IDs should be stable: %q != %q", id1, id2)
		}
	})

	t.Run("dev: different keys produce different IDs", func(t *testing.T) {
		t.Parallel()
		id1 := devServer.bundleID("about", "js", "")
		id2 := devServer.bundleID("blog", "js", "")
		if id1 == id2 {
			t.Errorf("dev IDs for different keys should differ: both %q", id1)
		}
	})

	t.Run("prod: same content produces same ID", func(t *testing.T) {
		t.Parallel()
		id1 := prodServer.bundleID("about", "js-content", "css-content")
		id2 := prodServer.bundleID("blog", "js-content", "css-content")
		if id1 != id2 {
			t.Errorf("prod IDs with same content should match: %q != %q", id1, id2)
		}
	})

	t.Run("prod: different JS produces different ID", func(t *testing.T) {
		t.Parallel()
		id1 := prodServer.bundleID("about", "js-v1", "css")
		id2 := prodServer.bundleID("about", "js-v2", "css")
		if id1 == id2 {
			t.Errorf("prod IDs with different JS should differ: both %q", id1)
		}
	})

	t.Run("prod: different CSS produces different ID", func(t *testing.T) {
		t.Parallel()
		id1 := prodServer.bundleID("about", "js", "css-v1")
		id2 := prodServer.bundleID("about", "js", "css-v2")
		if id1 == id2 {
			t.Errorf("prod IDs with different CSS should differ: both %q", id1)
		}
	})

	t.Run("prod: JS+CSS collision prevention", func(t *testing.T) {
		t.Parallel()
		id1 := prodServer.bundleID("k", "ab", "c")
		id2 := prodServer.bundleID("k", "a", "bc")
		if id1 == id2 {
			t.Errorf("null-byte separator should prevent hash collision: both %q", id1)
		}
	})

	t.Run("ID is 16 hex chars", func(t *testing.T) {
		t.Parallel()
		id := prodServer.bundleID("key", "js", "css")
		if len(id) != 16 {
			t.Errorf("bundleID length = %d, want 16", len(id))
		}
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				t.Errorf("bundleID contains non-hex character %q", c)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// createChain / middleware
// ---------------------------------------------------------------------------
func TestBuildChain(t *testing.T) {
	t.Parallel()

	t.Run("no middleware calls inner handler", func(t *testing.T) {
		t.Parallel()
		inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})
		h := createChain(inner, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("got %d, want %d", rec.Code, http.StatusTeapot)
		}
	})

	t.Run("middleware runs in FIFO order", func(t *testing.T) {
		t.Parallel()
		var order []string
		make := func(name string) func(http.Handler) http.Handler {
			return func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					order = append(order, name)
					next.ServeHTTP(w, r)
				})
			}
		}
		inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			order = append(order, "inner")
		})
		h := createChain(inner, []func(http.Handler) http.Handler{
			make("first"), make("second"),
		})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		want := "first,second,inner"
		if got := strings.Join(order, ","); got != want {
			t.Errorf("order = %q, want %q", got, want)
		}
	})

	t.Run("middleware can short-circuit", func(t *testing.T) {
		t.Parallel()
		authMW := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Token") != "secret" {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
			})
		}
		inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		h := createChain(inner, []func(http.Handler) http.Handler{authMW})

		// no token → 403
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("want 403, got %d", rec.Code)
		}

		// correct token → 200
		rec = httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Token", "secret")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("want 200, got %d", rec.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// handleHealth
// ---------------------------------------------------------------------------
func TestHandleHealth(t *testing.T) {
	t.Parallel()

	s := &Server{logger: nil}
	rec := httptest.NewRecorder()
	s.handleHealth(rec, httptest.NewRequest("GET", "/_echo/health", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
	if body.Version == "" {
		t.Error("version should be non-empty")
	}
}

// ---------------------------------------------------------------------------
// extractPathParams
// ---------------------------------------------------------------------------
func TestExtractPathParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pattern string
		url     string
		params  map[string]string
		want    map[string]string
	}{
		{
			name:    "static route",
			pattern: "/about",
			url:     "/about",
			params:  nil,
			want:    map[string]string{},
		},
		{
			name:    "single dynamic param",
			pattern: "/blog/{id}",
			url:     "/blog/42",
			params:  map[string]string{"id": "42"},
			want:    map[string]string{"id": "42"},
		},
		{
			name:    "catch-all param",
			pattern: "/docs/{slug...}",
			url:     "/docs/getting-started",
			params:  map[string]string{"slug": "getting-started"},
			want:    map[string]string{"slug": "getting-started"},
		},
		{
			name:    "multiple params",
			pattern: "/a/{x}/b/{y}",
			url:     "/a/1/b/2",
			params:  map[string]string{"x": "1", "y": "2"},
			want:    map[string]string{"x": "1", "y": "2"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest("GET", tc.url, nil)
			for k, v := range tc.params {
				req.SetPathValue(k, v)
			}
			got := extractPathParams(tc.pattern, req)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("param[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// normalizePaths
// ---------------------------------------------------------------------------
func TestNormalizePaths(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	s := &Server{appDir: appDir}

	t.Run("empty list", func(t *testing.T) {
		t.Parallel()
		got := s.normalizePaths(nil)
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})

	t.Run("empty string filtered out", func(t *testing.T) {
		t.Parallel()
		got := s.normalizePaths([]string{""})
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})

	t.Run("absolute path preserved", func(t *testing.T) {
		t.Parallel()
		abs := filepath.Join(appDir, "pages", "index.tsx")
		key := filepath.ToSlash(abs)
		got := s.normalizePaths([]string{abs})
		if _, ok := got[key]; !ok {
			t.Errorf("expected %q in result, got %v", key, got)
		}
	})

	t.Run("relative path joined with appDir", func(t *testing.T) {
		t.Parallel()
		want := filepath.ToSlash(filepath.Join(appDir, "pages", "index.tsx"))
		got := s.normalizePaths([]string{"pages/index.tsx"})
		if _, ok := got[want]; !ok {
			t.Errorf("expected %q in result, got %v", want, got)
		}
	})

	t.Run("deduplication", func(t *testing.T) {
		t.Parallel()
		abs := filepath.Join(appDir, "pages", "a.tsx")
		got := s.normalizePaths([]string{abs, abs})
		if len(got) != 1 {
			t.Errorf("expected 1 entry after dedup, got %d", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// injectLoaderData
// ---------------------------------------------------------------------------
func TestInjectLoaderData(t *testing.T) {
	t.Parallel()

	t.Run("injects before </body>", func(t *testing.T) {
		t.Parallel()
		shell := "<html><body><div id=\"root\"></div></body></html>"
		data := json.RawMessage(`{"key":"value"}`)
		result := injectLoaderData(shell, data)
		if !strings.Contains(result, `<script id="__echo_data__" type="application/json">{"key":"value"}</script>`) {
			t.Errorf("script tag not found in result: %s", result)
		}
		if !strings.HasSuffix(strings.TrimSpace(result), "</body></html>") {
			t.Errorf("</body> should follow script tag: %s", result)
		}
	})

	t.Run("no </body> tag leaves shell unchanged", func(t *testing.T) {
		t.Parallel()
		shell := "<div>no body tag</div>"
		data := json.RawMessage(`{}`)
		result := injectLoaderData(shell, data)
		if result != shell {
			t.Errorf("shell without </body> should be unchanged, got: %s", result)
		}
	})
}

// ---------------------------------------------------------------------------
// queryToMap
// ---------------------------------------------------------------------------
func TestQueryToMap(t *testing.T) {
	t.Parallel()

	t.Run("single value", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/?foo=bar", nil)
		got := queryToMap(req.URL.Query())
		if got["foo"] != "bar" {
			t.Errorf("foo = %q, want bar", got["foo"])
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/?a=1&b=2", nil)
		got := queryToMap(req.URL.Query())
		if got["a"] != "1" || got["b"] != "2" {
			t.Errorf("got %v, want a=1 b=2", got)
		}
	})

	t.Run("multi-value key uses first", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/?x=first&x=second", nil)
		got := queryToMap(req.URL.Query())
		if got["x"] != "first" {
			t.Errorf("x = %q, want first", got["x"])
		}
	})

	t.Run("empty query", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("GET", "/", nil)
		got := queryToMap(req.URL.Query())
		if len(got) != 0 {
			t.Errorf("expected empty map, got %v", got)
		}
	})
}

// ---------------------------------------------------------------------------
// gzipMiddleware
// ---------------------------------------------------------------------------
func TestGzipMiddleware(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello world")
	})
	h := gzipMiddleware(inner)

	t.Run("no Accept-Encoding: not compressed", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if enc := rec.Header().Get("Content-Encoding"); enc == "gzip" {
			t.Error("expected no gzip encoding without Accept-Encoding header")
		}
		if rec.Body.String() != "hello world" {
			t.Errorf("body = %q, want %q", rec.Body.String(), "hello world")
		}
	})

	t.Run("Accept-Encoding: gzip compresses response", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/page", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, req)
		if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
			t.Errorf("Content-Encoding = %q, want gzip", enc)
		}
		gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		defer gr.Close()
		body, _ := io.ReadAll(gr)
		if string(body) != "hello world" {
			t.Errorf("decompressed body = %q, want %q", string(body), "hello world")
		}
	})

	t.Run("SSE path skipped even with Accept-Encoding", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/_echo/sse", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(rec, req)
		if enc := rec.Header().Get("Content-Encoding"); enc == "gzip" {
			t.Error("SSE path should not be gzip-compressed")
		}
	})
}

// ---------------------------------------------------------------------------
// headersMiddleware
// ---------------------------------------------------------------------------
func TestHeadersMiddleware(t *testing.T) {
	t.Parallel()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := headersMiddleware(map[string]string{
		"X-Frame-Options": "DENY",
		"X-Custom":        "echo",
	})(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("X-Custom"); got != "echo" {
		t.Errorf("X-Custom = %q, want echo", got)
	}
}

// ---------------------------------------------------------------------------
// handlePage with Go loader
// ---------------------------------------------------------------------------
func TestHandlePage_WithGoLoader(t *testing.T) {
	t.Parallel()

	shell := "<html><body><div id=\"root\"></div></body></html>"
	cp := compiledPage{
		route: router.Route{Pattern: "/hello", BundleKey: "hello"},
		shell: shell,
	}

	s := &Server{
		goLoaders: map[string]LoaderFunc{
			"/hello": func(_ *http.Request) (any, error) {
				return map[string]string{"msg": "hi"}, nil
			},
		},
		jsLoaders: make(map[string]*loader.Loader),
	}

	rec := httptest.NewRecorder()
	s.handlePage(rec, httptest.NewRequest("GET", "/hello", nil), cp, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `__echo_data__`) {
		t.Error("expected __echo_data__ script tag in response")
	}
	if !strings.Contains(body, `"msg"`) {
		t.Error("expected loader data in response")
	}
}

// ---------------------------------------------------------------------------
// createStaticHandler — no public dir
// ---------------------------------------------------------------------------
func TestCreateStaticHandler_NoPublicDir(t *testing.T) {
	t.Parallel()

	s := &Server{
		appDir:  t.TempDir(),
		devMode: true,
	}

	h := s.createStaticHandler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/logo.png", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// handleError
// ---------------------------------------------------------------------------
func TestHandleError_NoErrorPage(t *testing.T) {
	t.Parallel()

	s := &Server{pages: nil}
	rec := httptest.NewRecorder()
	s.handleError(rec, httptest.NewRequest("GET", "/boom", nil), http.StatusInternalServerError, fmt.Errorf("something broke"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	// Falls back to plain text error.
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
}

func TestHandleError_WithErrorPage(t *testing.T) {
	t.Parallel()

	shell := "<html><body><div id=\"root\"></div></body></html>"
	s := &Server{
		pages: []compiledPage{
			{
				route: router.Route{Pattern: "/500", BundleKey: "500"},
				shell: shell,
			},
		},
	}

	rec := httptest.NewRecorder()
	s.handleError(rec, httptest.NewRequest("GET", "/crash", nil), http.StatusInternalServerError, fmt.Errorf("db connection failed"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `__echo_data__`) {
		t.Error("expected __echo_data__ script tag in 500 response")
	}
	if !strings.Contains(body, "db connection failed") {
		t.Error("expected error message in injected data")
	}
	if !strings.Contains(body, "/crash") {
		t.Error("expected request path in injected data")
	}
}

// ---------------------------------------------------------------------------
// recoverMiddleware
// ---------------------------------------------------------------------------
func TestRecoverMiddleware_NoPanic(t *testing.T) {
	t.Parallel()

	s := &Server{pages: nil}
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.recoverMiddleware(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRecoverMiddleware_PanicFallsBackToPlainError(t *testing.T) {
	t.Parallel()

	s := &Server{pages: nil}
	s.logger = slog.Default()

	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})
	h := s.recoverMiddleware(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestRecoverMiddleware_PanicRendersErrorPage(t *testing.T) {
	t.Parallel()

	shell := "<html><body><div id=\"root\"></div></body></html>"
	s := &Server{
		pages: []compiledPage{
			{route: router.Route{Pattern: "/500", BundleKey: "500"}, shell: shell},
		},
	}
	s.logger = slog.Default()

	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic(errors.New("exploded"))
	})
	h := s.recoverMiddleware(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "__echo_data__") {
		t.Error("expected error page HTML with __echo_data__")
	}
}
