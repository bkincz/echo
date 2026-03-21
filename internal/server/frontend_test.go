package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/echo-ssr/echo/internal/frontend"
	"github.com/echo-ssr/echo/internal/loader"
	"github.com/echo-ssr/echo/internal/nodeproc"
	"github.com/echo-ssr/echo/internal/router"
)

type fakeFrontendEngine struct {
	validateErr    error
	startDevErr    error
	buildClientErr error
	buildServerErr error
	renderErr      error
	renderResult   frontend.RenderResult
	renderFn       func(frontend.RenderOptions) (frontend.RenderResult, error)

	validateCalls    int
	startDevCalls    int
	buildClientCalls int
	buildServerCalls int
	renderCalls      int

	lastDevOptions    frontend.DevOptions
	lastClientOptions frontend.BuildClientOptions
	lastServerOptions frontend.BuildServerOptions
	lastRenderOptions frontend.RenderOptions
	renderHistory     []frontend.RenderOptions
}

func (f *fakeFrontendEngine) Name() string {
	return "fake"
}

func (f *fakeFrontendEngine) Validate(string) error {
	f.validateCalls++
	return f.validateErr
}

func (f *fakeFrontendEngine) StartDev(_ context.Context, _ string, opts frontend.DevOptions) (*nodeproc.Process, error) {
	f.startDevCalls++
	f.lastDevOptions = opts
	return nil, f.startDevErr
}

func (f *fakeFrontendEngine) BuildClient(_ context.Context, _ string, opts frontend.BuildClientOptions) error {
	f.buildClientCalls++
	f.lastClientOptions = opts
	return f.buildClientErr
}

func (f *fakeFrontendEngine) BuildServer(_ context.Context, appDir string, opts frontend.BuildServerOptions) error {
	f.buildServerCalls++
	f.lastServerOptions = opts
	if f.buildServerErr != nil {
		return f.buildServerErr
	}

	if opts.OutDir != "" && opts.SSREntry != "" {
		outDir := opts.OutDir
		if !filepath.IsAbs(outDir) {
			outDir = filepath.Join(appDir, outDir)
		}
		if err := os.MkdirAll(outDir, 0o755); err == nil {
			base := strings.TrimSuffix(filepath.Base(opts.SSREntry), filepath.Ext(opts.SSREntry))
			_ = os.WriteFile(filepath.Join(outDir, base+".js"), []byte("export function render(){}"), 0o644)
		}
	}
	return nil
}

func (f *fakeFrontendEngine) Render(_ context.Context, _ string, opts frontend.RenderOptions) (frontend.RenderResult, error) {
	f.renderCalls++
	f.lastRenderOptions = opts
	f.renderHistory = append(f.renderHistory, opts)
	if f.renderFn != nil {
		return f.renderFn(opts)
	}
	if f.renderResult.HTML == "" {
		f.renderResult.HTML = "<html><body>rendered</body></html>"
	}
	return f.renderResult, f.renderErr
}

func (f *fakeFrontendEngine) Close() error {
	return nil
}

// fakeStreamingEngine embeds fakeFrontendEngine and adds RenderStream, making
// it satisfy frontend.StreamingEngine. This lets tests exercise the streaming
// branch in handlePage without a real Node.js worker.
type fakeStreamingEngine struct {
	fakeFrontendEngine
	streamFn          func(opts frontend.RenderOptions, w io.Writer) error
	streamCalls       int
	lastStreamOptions frontend.RenderOptions
}

func (f *fakeStreamingEngine) RenderStream(_ context.Context, _ string, opts frontend.RenderOptions, w io.Writer) error {
	f.streamCalls++
	f.lastStreamOptions = opts
	if f.streamFn != nil {
		return f.streamFn(opts, w)
	}
	_, _ = io.WriteString(w, "<html><body>streamed</body></html>")
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestResolveFrontendEngine(t *testing.T) {
	t.Parallel()

	t.Run("returns provided engine", func(t *testing.T) {
		t.Parallel()
		fake := &fakeFrontendEngine{}
		got := resolveFrontendEngine(fake, discardLogger(), 0)
		if got != fake {
			t.Fatalf("expected provided engine pointer to be returned")
		}
	})

	t.Run("defaults to vite engine when nil", func(t *testing.T) {
		t.Parallel()
		got := resolveFrontendEngine(nil, discardLogger(), 0)
		if got == nil {
			t.Fatal("expected default frontend engine, got nil")
		}
		if got.Name() != "vite" {
			t.Fatalf("engine name = %q, want %q", got.Name(), "vite")
		}
	})
}

func TestRunFrontendBuild(t *testing.T) {
	t.Parallel()

	t.Run("validate error returns failure and short-circuits build calls", func(t *testing.T) {
		t.Parallel()

		fake := &fakeFrontendEngine{validateErr: errors.New("missing runtime")}
		_, err := runFrontendBuild(t.TempDir(), discardLogger(), BuildOptions{
			Frontend:         fake,
			FrontendSSREntry: "src/entry-server.tsx",
		})
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if !strings.Contains(err.Error(), "frontend validation failed (fake): missing runtime") {
			t.Fatalf("unexpected error: %v", err)
		}

		if fake.validateCalls != 1 {
			t.Fatalf("validate calls = %d, want 1", fake.validateCalls)
		}
		if fake.buildClientCalls != 0 {
			t.Fatalf("client build calls = %d, want 0", fake.buildClientCalls)
		}
		if fake.buildServerCalls != 0 {
			t.Fatalf("server build calls = %d, want 0", fake.buildServerCalls)
		}
	})

	t.Run("forwards client and server build options", func(t *testing.T) {
		t.Parallel()

		fake := &fakeFrontendEngine{}
		result, err := runFrontendBuild(t.TempDir(), discardLogger(), BuildOptions{
			Frontend:             fake,
			FrontendClientOutDir: "dist/client",
			FrontendServerOutDir: "dist/server",
			FrontendSSREntry:     "src/entry-server.tsx",
		})
		if err != nil {
			t.Fatalf("runFrontendBuild: %v", err)
		}

		if fake.validateCalls != 1 {
			t.Fatalf("validate calls = %d, want 1", fake.validateCalls)
		}
		if fake.buildClientCalls != 1 {
			t.Fatalf("client build calls = %d, want 1", fake.buildClientCalls)
		}
		if fake.buildServerCalls != 1 {
			t.Fatalf("server build calls = %d, want 1", fake.buildServerCalls)
		}
		if fake.lastClientOptions.OutDir != "dist/client" {
			t.Fatalf("client outDir = %q, want %q", fake.lastClientOptions.OutDir, "dist/client")
		}
		if fake.lastServerOptions.OutDir != "dist/server" {
			t.Fatalf("server outDir = %q, want %q", fake.lastServerOptions.OutDir, "dist/server")
		}
		if fake.lastServerOptions.SSREntry != "src/entry-server.tsx" {
			t.Fatalf("server ssrEntry = %q, want %q", fake.lastServerOptions.SSREntry, "src/entry-server.tsx")
		}
		if result.Engine != "fake" {
			t.Fatalf("engine = %q, want %q", result.Engine, "fake")
		}
		if result.SSREntry != "dist/server/entry-server.js" {
			t.Fatalf("ssr entry = %q, want %q", result.SSREntry, "dist/server/entry-server.js")
		}
	})

	t.Run("client build failure returns error and stops server build", func(t *testing.T) {
		t.Parallel()

		fake := &fakeFrontendEngine{
			buildClientErr: errors.New("client build failed"),
		}
		_, err := runFrontendBuild(t.TempDir(), discardLogger(), BuildOptions{
			Frontend:         fake,
			FrontendSSREntry: "src/entry-server.tsx",
		})
		if err == nil {
			t.Fatal("expected client build error, got nil")
		}
		if !strings.Contains(err.Error(), "frontend client build failed (fake): client build failed") {
			t.Fatalf("unexpected error: %v", err)
		}

		if fake.buildClientCalls != 1 {
			t.Fatalf("client build calls = %d, want 1", fake.buildClientCalls)
		}
		if fake.buildServerCalls != 0 {
			t.Fatalf("server build calls = %d, want 0", fake.buildServerCalls)
		}
	})

	t.Run("missing frontend ssr entry returns actionable error", func(t *testing.T) {
		t.Parallel()

		fake := &fakeFrontendEngine{}
		_, err := runFrontendBuild(t.TempDir(), discardLogger(), BuildOptions{
			Frontend: fake,
		})
		if err == nil {
			t.Fatal("expected missing ssr entry error, got nil")
		}
		if !strings.Contains(err.Error(), "frontend ssr entry not found") {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(err.Error(), "src/entry-server.tsx") {
			t.Fatalf("expected error to include expected entry filenames, got: %v", err)
		}
		if fake.buildClientCalls != 0 {
			t.Fatalf("client build calls = %d, want 0", fake.buildClientCalls)
		}
		if fake.buildServerCalls != 0 {
			t.Fatalf("server build calls = %d, want 0", fake.buildServerCalls)
		}
	})
}

func TestHandlePageDevUsesFrontendRender(t *testing.T) {
	t.Parallel()

	fake := &fakeFrontendEngine{
		renderResult: frontend.RenderResult{HTML: "<html><body>SSR</body></html>"},
	}

	s := &Server{
		appDir:      t.TempDir(),
		devMode:     true,
		logger:      discardLogger(),
		goLoaders:   map[string]LoaderFunc{},
		jsLoaders:   map[string]*loader.Loader{},
		frontend:    fake,
		frontendSSR: "src/entry-server.tsx",
	}
	s.goLoaders["/"] = func(_ *http.Request) (any, error) {
		return map[string]string{"message": "ok"}, nil
	}

	page := compiledPage{
		route: router.Route{
			Pattern:   "/",
			BundleKey: "index",
		},
		shell: "<html><body><div id=\"app\"></div></body></html>",
	}

	req := httptest.NewRequest(http.MethodGet, "/products?x=1", nil)
	rec := httptest.NewRecorder()
	s.handlePage(rec, req, page, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<html><body>SSR</body></html>" {
		t.Fatalf("body = %q, want SSR html", body)
	}
	if fake.renderCalls != 1 {
		t.Fatalf("render calls = %d, want 1", fake.renderCalls)
	}
	if fake.lastRenderOptions.SSREntry != "src/entry-server.tsx" {
		t.Fatalf("ssr entry = %q, want %q", fake.lastRenderOptions.SSREntry, "src/entry-server.tsx")
	}
	if fake.lastRenderOptions.RoutePattern != "/" {
		t.Fatalf("route pattern = %q, want /", fake.lastRenderOptions.RoutePattern)
	}
	if fake.lastRenderOptions.URL != "/products?x=1" {
		t.Fatalf("url = %q, want %q", fake.lastRenderOptions.URL, "/products?x=1")
	}
	var loaderPayload map[string]string
	if err := json.Unmarshal(fake.lastRenderOptions.LoaderData, &loaderPayload); err != nil {
		t.Fatalf("unmarshal loader data: %v", err)
	}
	if loaderPayload["message"] != "ok" {
		t.Fatalf("loader message = %q, want %q", loaderPayload["message"], "ok")
	}
}

func TestHandlePageProductionUsesFrontendRenderWhenSSREntryConfigured(t *testing.T) {
	t.Parallel()

	fake := &fakeFrontendEngine{
		renderResult: frontend.RenderResult{HTML: "<html><body>SSR PROD</body></html>"},
	}

	s := &Server{
		appDir:      t.TempDir(),
		devMode:     false,
		logger:      discardLogger(),
		goLoaders:   map[string]LoaderFunc{},
		jsLoaders:   map[string]*loader.Loader{},
		frontend:    fake,
		frontendSSR: "dist/server/entry-server.js",
	}
	s.goLoaders["/"] = func(_ *http.Request) (any, error) {
		return map[string]string{"message": "prod"}, nil
	}

	page := compiledPage{
		route: router.Route{
			Pattern:   "/",
			BundleKey: "index",
		},
		shell: "<html><body><div id=\"app\"></div></body></html>",
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handlePage(rec, req, page, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<html><body>SSR PROD</body></html>" {
		t.Fatalf("body = %q, want production SSR html", body)
	}
	if fake.renderCalls != 1 {
		t.Fatalf("render calls = %d, want 1", fake.renderCalls)
	}
	if fake.lastRenderOptions.SSREntry != "dist/server/entry-server.js" {
		t.Fatalf("ssr entry = %q, want dist/server/entry-server.js", fake.lastRenderOptions.SSREntry)
	}
}

func TestDetectBuiltSSREntry(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	outDir := filepath.Join(appDir, "dist", "server")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	entryPath := filepath.Join(outDir, "entry-server.js")
	if err := os.WriteFile(entryPath, []byte("export function render(){}"), 0o644); err != nil {
		t.Fatalf("write entry: %v", err)
	}

	got, err := detectBuiltSSREntry(appDir, "dist/server", "src/entry-server.tsx")
	if err != nil {
		t.Fatalf("detectBuiltSSREntry: %v", err)
	}
	if got != "dist/server/entry-server.js" {
		t.Fatalf("got %q, want %q", got, "dist/server/entry-server.js")
	}
}

func TestHandlePageStreamingEnginePreferredOverRender(t *testing.T) {
	t.Parallel()

	engine := &fakeStreamingEngine{}
	s := &Server{
		appDir:      t.TempDir(),
		devMode:     true,
		logger:      discardLogger(),
		goLoaders:   map[string]LoaderFunc{},
		jsLoaders:   map[string]*loader.Loader{},
		frontend:    engine,
		frontendSSR: "src/entry-server.tsx",
	}

	page := compiledPage{
		route: router.Route{Pattern: "/", BundleKey: "index"},
		shell: `<html><body><div id="root"></div></body></html>`,
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handlePage(rec, req, page, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if engine.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want 1", engine.streamCalls)
	}
	if engine.renderCalls != 0 {
		t.Fatalf("render calls = %d, want 0 (streaming path should be taken)", engine.renderCalls)
	}
	if body := rec.Body.String(); body != "<html><body>streamed</body></html>" {
		t.Fatalf("body = %q, want streamed html", body)
	}
}

func TestHandlePageStreamingForwardsRenderOptions(t *testing.T) {
	t.Parallel()

	engine := &fakeStreamingEngine{}
	s := &Server{
		appDir:      t.TempDir(),
		devMode:     true,
		logger:      discardLogger(),
		goLoaders:   map[string]LoaderFunc{},
		jsLoaders:   map[string]*loader.Loader{},
		frontend:    engine,
		frontendSSR: "src/entry-server.tsx",
	}
	s.goLoaders["/"] = func(_ *http.Request) (any, error) {
		return map[string]string{"key": "val"}, nil
	}

	page := compiledPage{
		route: router.Route{Pattern: "/", BundleKey: "index"},
		shell: `<html><body><div id="root"></div></body></html>`,
	}

	req := httptest.NewRequest(http.MethodGet, "/?q=1", nil)
	rec := httptest.NewRecorder()
	s.handlePage(rec, req, page, http.StatusOK)

	opts := engine.lastStreamOptions
	if opts.SSREntry != "src/entry-server.tsx" {
		t.Fatalf("ssr entry = %q, want src/entry-server.tsx", opts.SSREntry)
	}
	if opts.RoutePattern != "/" {
		t.Fatalf("route pattern = %q, want /", opts.RoutePattern)
	}
	if opts.URL != "/?q=1" {
		t.Fatalf("url = %q, want /?q=1", opts.URL)
	}
	var data map[string]string
	if err := json.Unmarshal(opts.LoaderData, &data); err != nil {
		t.Fatalf("unmarshal loader data: %v", err)
	}
	if data["key"] != "val" {
		t.Fatalf("loader data key = %q, want val", data["key"])
	}
}

func TestHandleLoaderDataReturnsJSONFromGoLoader(t *testing.T) {
	t.Parallel()

	s := &Server{
		appDir:    t.TempDir(),
		logger:    discardLogger(),
		goLoaders: map[string]LoaderFunc{},
		jsLoaders: map[string]*loader.Loader{},
	}
	s.goLoaders["/blog/{id}"] = func(r *http.Request) (any, error) {
		return map[string]string{"id": r.PathValue("id"), "path": r.URL.Path}, nil
	}

	page := compiledPage{
		route: router.Route{Pattern: "/blog/{id}", BundleKey: "blog/[id]"},
	}

	req := httptest.NewRequest(http.MethodGet, "/_echo/data/blog/42?x=1", nil)
	req.SetPathValue("id", "42")
	rec := httptest.NewRecorder()
	s.handleLoaderData(rec, req, page)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["id"] != "42" {
		t.Errorf("id = %q, want 42", got["id"])
	}
	// URL should be rewritten to the original page path, not /_echo/data/...
	if got["path"] != "/blog/42" {
		t.Errorf("path = %q, want /blog/42 (URL should be rewritten)", got["path"])
	}
}

func TestHandleLoaderDataReturnsNullForPageWithNoLoader(t *testing.T) {
	t.Parallel()

	s := &Server{
		appDir:    t.TempDir(),
		logger:    discardLogger(),
		goLoaders: map[string]LoaderFunc{},
		jsLoaders: map[string]*loader.Loader{},
	}

	page := compiledPage{
		route: router.Route{Pattern: "/about", BundleKey: "about"},
	}

	req := httptest.NewRequest(http.MethodGet, "/_echo/data/about", nil)
	rec := httptest.NewRecorder()
	s.handleLoaderData(rec, req, page)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "null" {
		t.Fatalf("body = %q, want null", body)
	}
}

func TestHandleLoaderDataErrorReturns500(t *testing.T) {
	t.Parallel()

	s := &Server{
		appDir:    t.TempDir(),
		logger:    discardLogger(),
		goLoaders: map[string]LoaderFunc{},
		jsLoaders: map[string]*loader.Loader{},
	}
	s.goLoaders["/"] = func(_ *http.Request) (any, error) {
		return nil, errors.New("loader exploded")
	}

	page := compiledPage{
		route: router.Route{Pattern: "/", BundleKey: "index"},
	}

	req := httptest.NewRequest(http.MethodGet, "/_echo/data/", nil)
	rec := httptest.NewRecorder()
	s.handleLoaderData(rec, req, page)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestHandlePageStreamingErrorAfterHeadersIsLogged(t *testing.T) {
	t.Parallel()

	var logBuf strings.Builder
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	engine := &fakeStreamingEngine{
		streamFn: func(_ frontend.RenderOptions, w io.Writer) error {
			_, _ = io.WriteString(w, "<html>partial")
			return errors.New("stream broke mid-flight")
		},
	}
	s := &Server{
		appDir:      t.TempDir(),
		devMode:     true,
		logger:      logger,
		goLoaders:   map[string]LoaderFunc{},
		jsLoaders:   map[string]*loader.Loader{},
		frontend:    engine,
		frontendSSR: "src/entry-server.tsx",
	}

	page := compiledPage{
		route: router.Route{Pattern: "/", BundleKey: "index"},
		shell: `<html><body><div id="root"></div></body></html>`,
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handlePage(rec, req, page, http.StatusOK)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (headers already sent)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html>partial") {
		t.Fatalf("expected partial body in response, got: %q", rec.Body.String())
	}
	if !strings.Contains(logBuf.String(), "streaming render error") {
		t.Fatalf("expected 'streaming render error' log, got: %s", logBuf.String())
	}
}
