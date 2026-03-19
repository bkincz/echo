package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	esbuild "github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/bundler"
	"github.com/echo-ssr/echo/internal/config"
	"github.com/echo-ssr/echo/internal/layout"
	"github.com/echo-ssr/echo/internal/loader"
	"github.com/echo-ssr/echo/internal/plugins"
	"github.com/echo-ssr/echo/internal/renderer"
	"github.com/echo-ssr/echo/internal/router"
	"github.com/echo-ssr/echo/internal/watcher"
)

const version = "1.0.0"

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// PathsFunc returns all param combinations for a dynamic route during static
// builds. Each entry maps param name → value (e.g. {"id": "1"}).
// Register with Paths() before calling BuildStatic().
type PathsFunc func() ([]map[string]string, error)

// LoaderFunc is a server-side data loader for a page. The returned value is
// JSON-encoded and embedded in the HTML as <script id="__echo_data__"
// type="application/json">. Call Loader() to register one per route pattern.
// Loaders must be registered before Start() is called.
type LoaderFunc func(r *http.Request) (any, error)

type ServerOptions struct {
	// Middleware is applied to every HTTP request in the order provided.
	// The first entry is the outermost layer (runs first on the way in,
	// last on the way out). Each func must call its next handler.
	Middleware []func(http.Handler) http.Handler
	// Logger is used for all server-side log output. Defaults to slog.Default().
	Logger *slog.Logger

	// Plugins are passed directly to esbuild. We use them to add support for
	// non-standard file types (e.g. .svelte, .vue) by providing a custom
	// OnLoad handler for the relevant extensions.
	Plugins []esbuild.Plugin
}

type manifestRoute struct {
	Pattern     string `json:"pattern"`
	BundleKey   string `json:"bundleKey"`
	BundleID    string `json:"bundleID"`
	HasCSS      bool   `json:"hasCSS,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type manifest struct {
	Routes []manifestRoute `json:"routes"`
}

type buildResult struct {
	page   compiledPage
	js     string
	css    string
	inputs map[string]struct{}
	err    error
}

type changeResult struct {
	idx    int
	page   compiledPage
	js     string
	css    string
	inputs map[string]struct{}
	err    error
}

type compiledPage struct {
	route       router.Route
	bundleID    string
	hasCSS      bool
	title       string
	description string
	shell       string
}

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

type Server struct {
	appDir     string
	devMode    bool
	cfg        config.Config
	mu         sync.RWMutex
	pages      []compiledPage
	bundles    map[string]string
	inputs     map[string]map[string]struct{}
	jsLoaders  map[string]*loader.Loader
	apiRunners map[string]*loader.APIRunner
	handler    http.Handler
	chain      http.Handler
	logger     *slog.Logger
	compiler   *bundler.Compiler
	goLoaders  map[string]LoaderFunc
	goPaths    map[string]PathsFunc
	goHandlers map[string]http.Handler
	rebuildMu  sync.Mutex
	sseClients sync.Map
	w          *watcher.Watcher
}

func (s *Server) Paths(pattern string, fn PathsFunc) *Server {
	s.goPaths[pattern] = fn
	return s
}

func (s *Server) Handle(pattern string, h http.Handler) *Server {
	s.goHandlers[pattern] = h
	return s
}

func (s *Server) Loader(pattern string, fn LoaderFunc) *Server {
	s.goLoaders[pattern] = fn
	return s
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// autoPlugins detects framework-specific esbuild plugins from the project's
// node_modules and returns them. Auto-detected plugins run after any plugins
// supplied via ServerOptions, so user-provided plugins take precedence.

// NOTE: We need to expand and refactor this as we add support for more frameworks.
func autoPlugins(appDir string, logger *slog.Logger) []esbuild.Plugin {
	var detected []esbuild.Plugin
	if plugins.FindSvelte(appDir) {
		logger.Info("Svelte: plugin enabled")
		detected = append(detected, plugins.SveltePlugin(appDir))
	}
	if plugins.FindVue(appDir) {
		logger.Info("Vue: plugin enabled")
		detected = append(detected, plugins.VuePlugin(appDir))
	}
	return detected
}

func createChain(inner http.Handler, mw []func(http.Handler) http.Handler) http.Handler {
	h := inner
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

func (s *Server) initServer(opts ServerOptions) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s.logger = opts.Logger

	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		h := s.handler
		s.mu.RUnlock()
		h.ServeHTTP(w, r)
	})
	mw := []func(http.Handler) http.Handler{gzipMiddleware}
	if len(s.cfg.Headers) > 0 {
		mw = append(mw, headersMiddleware(s.cfg.Headers))
	}
	mw = append(mw, opts.Middleware...)
	s.chain = s.recoverMiddleware(createChain(core, mw))
}

func New(appDir string, devMode bool, opts ...ServerOptions) (*Server, error) {
	abs, err := filepath.Abs(appDir)
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(abs)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	opt := ServerOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}

	compilerOpts := bundler.Options{
		AppDir:  abs,
		Minify:  !devMode,
		Plugins: append(opt.Plugins, autoPlugins(abs, opt.Logger)...),
	}
	if lc := plugins.FindLightningCSS(abs); lc != "" {
		opt.Logger.Info("CSS: Lightning CSS enabled")
		compilerOpts.CSSTransform = plugins.LightningCSSTransform(lc, !devMode)
	}
	compiler, err := bundler.NewCompiler(compilerOpts)
	if err != nil {
		return nil, err
	}

	s := &Server{
		appDir:     abs,
		devMode:    devMode,
		cfg:        cfg,
		bundles:    make(map[string]string),
		inputs:     make(map[string]map[string]struct{}),
		jsLoaders:  make(map[string]*loader.Loader),
		apiRunners: make(map[string]*loader.APIRunner),
		goLoaders:  make(map[string]LoaderFunc),
		goPaths:    make(map[string]PathsFunc),
		goHandlers: make(map[string]http.Handler),
		compiler:   compiler,
	}
	s.buildJSLoaders()
	s.buildAPIRunners()

	if err := s.rebuild(); err != nil {
		s.compiler.Close()
		return nil, err
	}

	s.initServer(opt)
	return s, nil
}

func NewProduction(appDir string, opts ...ServerOptions) (*Server, error) {
	abs, err := filepath.Abs(appDir)
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(abs)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	distDir := filepath.Join(abs, "dist")
	data, err := os.ReadFile(filepath.Join(distDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("reading dist/manifest.json (run 'echo build %s' first): %w", appDir, err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest: %w", err)
	}

	pages := make([]compiledPage, 0, len(m.Routes))
	for _, mr := range m.Routes {
		cssBundleURL := ""
		if mr.HasCSS {
			cssBundleURL = "/_echo/bundle/" + mr.BundleID + ".css"
		}
		shell, err := renderer.Shell(renderer.ShellOptions{
			Title:        mr.Title,
			Description:  mr.Description,
			BundleURL:    "/_echo/bundle/" + mr.BundleID + ".js",
			CSSBundleURL: cssBundleURL,
			DevMode:      false,
		})
		if err != nil {
			return nil, fmt.Errorf("rendering shell for %s: %w", mr.Pattern, err)
		}
		pages = append(pages, compiledPage{
			route: router.Route{
				Pattern:   mr.Pattern,
				BundleKey: mr.BundleKey,
			},
			bundleID:    mr.BundleID,
			hasCSS:      mr.HasCSS,
			title:       mr.Title,
			description: mr.Description,
			shell:       shell,
		})
	}

	s := &Server{
		appDir:     abs,
		devMode:    false,
		cfg:        cfg,
		bundles:    make(map[string]string),
		inputs:     make(map[string]map[string]struct{}),
		jsLoaders:  make(map[string]*loader.Loader),
		apiRunners: make(map[string]*loader.APIRunner),
		goLoaders:  make(map[string]LoaderFunc),
		goPaths:    make(map[string]PathsFunc),
		goHandlers: make(map[string]http.Handler),
		pages:      pages,
	}
	s.buildJSLoaders()
	s.buildAPIRunners()
	s.handler = s.createMux(pages)

	opt := ServerOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	s.initServer(opt)
	return s, nil
}

func (s *Server) bundleID(key, js, css string) string {
	var src []byte
	if s.devMode {
		src = []byte(key)
	} else {
		src = []byte(js + "\x00" + css)
	}
	h := sha256.Sum256(src)
	return fmt.Sprintf("%x", h[:8])
}

// ---------------------------------------------------------------------------
// Build
// ---------------------------------------------------------------------------

func (s *Server) rebuild() error {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()
	return s.rebuildAllLocked()
}

func (s *Server) rebuildAllLocked() error {
	pagesDir := filepath.Join(s.appDir, "pages")
	routes, err := router.Scan(pagesDir)
	if err != nil {
		return fmt.Errorf("scanning pages: %w", err)
	}
	return s.compileAndSwapLocked(routes)
}

func (s *Server) compileAndSwapLocked(routes []router.Route) error {
	pagesDir := filepath.Join(s.appDir, "pages")
	layoutMap, _ := layout.Find(pagesDir)

	results := make([]buildResult, len(routes))
	var wg sync.WaitGroup
	for i, route := range routes {
		wg.Add(1)
		go func(i int, route router.Route) {
			defer wg.Done()
			s.logger.Info("bundling", "key", route.BundleKey, "pattern", route.Pattern)
			b, err := s.compiler.Build(route.FilePath, layoutMap[route.BundleKey])
			if err != nil {
				results[i].err = fmt.Errorf("bundling %s: %w", route.FilePath, err)
				return
			}
			id := s.bundleID(route.BundleKey, b.JS, b.CSS)
			title, description := readPageMeta(route.FilePath, route.Pattern)
			cssBundleURL := ""
			if b.CSS != "" {
				cssBundleURL = "/_echo/bundle/" + id + ".css"
			}
			shell, err := renderer.Shell(renderer.ShellOptions{
				Title:        title,
				Description:  description,
				BundleURL:    "/_echo/bundle/" + id + ".js",
				CSSBundleURL: cssBundleURL,
				DevMode:      s.devMode,
			})
			if err != nil {
				results[i].err = fmt.Errorf("rendering shell for %s: %w", route.FilePath, err)
				return
			}
			results[i] = buildResult{
				page:   compiledPage{route: route, bundleID: id, hasCSS: b.CSS != "", title: title, description: description, shell: shell},
				js:     b.JS,
				css:    b.CSS,
				inputs: toSet(b.Inputs),
			}
		}(i, route)
	}
	wg.Wait()

	pages := make([]compiledPage, 0, len(routes))
	bundles := make(map[string]string, len(routes))
	inputs := make(map[string]map[string]struct{}, len(routes))
	for _, res := range results {
		if res.err != nil {
			return res.err
		}
		pages = append(pages, res.page)
		bundles[res.page.bundleID+".js"] = res.js
		if res.css != "" {
			bundles[res.page.bundleID+".css"] = res.css
		}
		inputs[res.page.route.BundleKey] = res.inputs
	}

	handler := s.createMux(pages)

	s.mu.RLock()
	oldRoutes := routesFromPages(s.pages)
	s.mu.RUnlock()

	s.mu.Lock()
	s.pages = pages
	s.bundles = bundles
	s.inputs = inputs
	s.handler = handler
	s.mu.Unlock()

	s.disposeRemovedContexts(oldRoutes, routes)
	return nil
}

func (s *Server) buildAPIRunners() {
	pagesDir := filepath.Join(s.appDir, "pages")
	routes, err := router.ScanAPI(pagesDir)
	if err != nil {
		s.logger.Warn("scanning api routes", "err", err)
		return
	}
	for _, route := range routes {
		r := route
		runner, err := loader.BuildAPI(s.appDir, r.FilePath)
		if err != nil {
			s.logger.Error("building api handler", "key", r.BundleKey, "err", err)
			continue
		}
		s.mu.Lock()
		if old, ok := s.apiRunners[r.Pattern]; ok {
			old.Close()
		}
		s.apiRunners[r.Pattern] = runner
		s.mu.Unlock()
		s.logger.Info("api handler ready", "pattern", r.Pattern)
	}
}

func (s *Server) buildJSLoaders() {
	pagesDir := filepath.Join(s.appDir, "pages")
	found, err := loader.Find(pagesDir)
	if err != nil {
		s.logger.Warn("scanning loaders", "err", err)
		return
	}
	for key, filePath := range found {
		s.rebuildJSLoader(key, filePath)
	}
}

func (s *Server) rebuildJSLoader(key, filePath string) {
	l, err := loader.Build(s.appDir, filePath)
	if err != nil {
		s.logger.Error("building loader", "key", key, "err", err)
		return
	}
	s.mu.Lock()
	if old, ok := s.jsLoaders[key]; ok {
		old.Close()
	}
	s.jsLoaders[key] = l
	s.mu.Unlock()
	s.logger.Info("loader ready", "key", key)
}

func (s *Server) handleChanges(paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()

	pagesDir := filepath.Join(s.appDir, "pages")
	routes, err := router.Scan(pagesDir)
	if err != nil {
		return fmt.Errorf("scanning pages: %w", err)
	}

	s.mu.RLock()
	oldRoutes := routesFromPages(s.pages)
	oldBundles := cloneStringMap(s.bundles)
	oldInputs := cloneInputSetMap(s.inputs)
	oldPages := make([]compiledPage, len(s.pages))
	copy(oldPages, s.pages)
	s.mu.RUnlock()

	if routesChanged(oldRoutes, routes) {
		return s.compileAndSwapLocked(routes)
	}

	changed := s.normalizePaths(paths)
	affected := affectedBundleKeys(routes, oldInputs, changed)
	if len(affected) == 0 {
		return nil
	}

	newPages := make([]compiledPage, len(oldPages))
	copy(newPages, oldPages)
	newBundles := cloneStringMap(oldBundles)
	newInputs := cloneInputSetMap(oldInputs)

	layoutMap, _ := layout.Find(pagesDir)

	// If any changed file is a layout, do a full rebuild so the layout chain
	// is recomputed for all affected pages.
	for p := range changed {
		if layout.IsLayoutFile(p) {
			return s.compileAndSwapLocked(routes)
		}
	}

	var (
		changesMu sync.Mutex
		changesWg sync.WaitGroup
		changes   []changeResult
	)
	for i, page := range newPages {
		if _, ok := affected[page.route.BundleKey]; !ok {
			continue
		}
		changesWg.Add(1)
		go func(i int, page compiledPage) {
			defer changesWg.Done()
			s.logger.Info("rebundling", "key", page.route.BundleKey, "pattern", page.route.Pattern)
			b, err := s.compiler.Build(page.route.FilePath, layoutMap[page.route.BundleKey])
			if err != nil {
				changesMu.Lock()
				changes = append(changes, changeResult{err: fmt.Errorf("bundling %s: %w", page.route.FilePath, err)})
				changesMu.Unlock()
				return
			}
			id := s.bundleID(page.route.BundleKey, b.JS, b.CSS)
			title, description := readPageMeta(page.route.FilePath, page.route.Pattern)
			cssBundleURL := ""
			if b.CSS != "" {
				cssBundleURL = "/_echo/bundle/" + id + ".css"
			}
			shell, err := renderer.Shell(renderer.ShellOptions{
				Title:        title,
				Description:  description,
				BundleURL:    "/_echo/bundle/" + id + ".js",
				CSSBundleURL: cssBundleURL,
				DevMode:      s.devMode,
			})
			if err != nil {
				changesMu.Lock()
				changes = append(changes, changeResult{err: fmt.Errorf("rendering shell for %s: %w", page.route.FilePath, err)})
				changesMu.Unlock()
				return
			}
			changesMu.Lock()
			changes = append(changes, changeResult{
				idx:    i,
				page:   compiledPage{route: page.route, bundleID: id, hasCSS: b.CSS != "", title: title, description: description, shell: shell},
				js:     b.JS,
				css:    b.CSS,
				inputs: toSet(b.Inputs),
			})
			changesMu.Unlock()
		}(i, page)
	}
	changesWg.Wait()

	for _, res := range changes {
		if res.err != nil {
			return res.err
		}
		oldID := newPages[res.idx].bundleID
		delete(newBundles, oldID+".js")
		delete(newBundles, oldID+".css")
		newPages[res.idx] = res.page
		newBundles[res.page.bundleID+".js"] = res.js
		if res.css != "" {
			newBundles[res.page.bundleID+".css"] = res.css
		}
		newInputs[res.page.route.BundleKey] = res.inputs
	}

	handler := s.createMux(newPages)

	s.mu.Lock()
	s.pages = newPages
	s.bundles = newBundles
	s.inputs = newInputs
	s.handler = handler
	s.mu.Unlock()

	// Rebuild JS loaders for any changed loader source files.
	loaderFiles, err := loader.Find(pagesDir)
	if err == nil {
		for key, filePath := range loaderFiles {
			normalized := filepath.ToSlash(filepath.Clean(filePath))
			if _, ok := changed[normalized]; ok {
				go s.rebuildJSLoader(key, filePath)
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

// createMux constructs a fresh ServeMux for the current compiled pages.
// Precedence (high → low):
//  1. /_echo/*       — internal framework endpoints
//  2. Page patterns  — e.g. GET /about, GET /blog/{id}
//  3. GET /{$}       — exact root match (index page)
//  4. GET /          — static files + 404 fallback
func (s *Server) createMux(pages []compiledPage) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /_echo/health", s.handleHealth)

	// Go API handlers (registered before page routes so they take precedence).
	for pattern, h := range s.goHandlers {
		mux.Handle(pattern, h)
	}

	// JS API runners.
	s.mu.RLock()
	for pat, run := range s.apiRunners {
		pat, run := pat, run
		mux.HandleFunc(pat, func(w http.ResponseWriter, r *http.Request) {
			s.handleAPIRunner(w, r, pat, run)
		})
	}
	s.mu.RUnlock()

	if s.devMode {
		mux.HandleFunc("GET /_echo/bundle/", s.handleBundle)
		mux.HandleFunc("GET /_echo/sse", s.handleSSE)
	} else {
		bundleDir := filepath.Join(s.appDir, "dist", "_echo", "bundle")
		bundleServer := http.StripPrefix("/_echo/bundle/", http.FileServer(noDirListingFS{http.Dir(bundleDir)}))
		mux.Handle("GET /_echo/bundle/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			if strings.HasSuffix(r.URL.Path, ".css") {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			bundleServer.ServeHTTP(w, r)
		}))
	}

	for _, p := range pages {
		cp := p
		pattern := "GET " + cp.route.Pattern
		if cp.route.Pattern == "/" {
			pattern = "GET /{$}"
		}
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			s.handlePage(w, r, cp, http.StatusOK)
		})
	}

	mux.Handle("GET /", s.createStaticHandler(pages))
	return mux
}

func (s *Server) createStaticHandler(pages []compiledPage) http.Handler {
	publicDir := filepath.Join(s.appDir, "public")
	if !s.devMode {
		publicDir = filepath.Join(s.appDir, "dist", "public")
	}

	var notFoundPage *compiledPage
	for i := range pages {
		if pages[i].route.BundleKey == "404" {
			cp := pages[i]
			notFoundPage = &cp
			break
		}
	}

	serve404 := func(w http.ResponseWriter, r *http.Request) {
		if notFoundPage != nil {
			s.handlePage(w, r, *notFoundPage, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}

	if _, err := os.Stat(publicDir); err != nil {
		return http.HandlerFunc(serve404)
	}

	fileServer := http.FileServer(noDirListingFS{http.Dir(publicDir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := path.Clean("/" + r.URL.Path)
		fpath := filepath.Join(publicDir, filepath.FromSlash(cleanPath))

		info, statErr := os.Stat(fpath)
		if statErr != nil {
			serve404(w, r)
			return
		}
		if info.IsDir() {
			indexPath := filepath.Join(fpath, "index.html")
			if _, err := os.Stat(indexPath); err != nil {
				serve404(w, r)
				return
			}
			http.ServeFile(w, r, indexPath)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

type noDirListingFS struct {
	http.FileSystem
}

func (n noDirListingFS) Open(name string) (http.File, error) {
	f, err := n.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.chain.ServeHTTP(w, r)
}

func (s *Server) Start(addr string) error {
	if s.compiler != nil {
		defer s.compiler.Close()
	}
	defer func() {
		s.mu.Lock()
		for _, l := range s.jsLoaders {
			l.Close()
		}
		for _, a := range s.apiRunners {
			a.Close()
		}
		s.mu.Unlock()
	}()

	if s.devMode {
		pagesDir := filepath.Join(s.appDir, "pages")
		publicDir := filepath.Join(s.appDir, "public")

		w, err := watcher.New(func(paths []string) {
			go func() {
				s.logger.Info("change detected, recompiling", "files", len(paths))
				if err := s.handleChanges(paths); err != nil {
					s.logger.Error("rebuild error", "err", err)
					s.broadcastError(err.Error())
					return
				}
				s.syncWatchedDirs()
				s.broadcastReload()
			}()
		}, pagesDir, publicDir)
		if err != nil {
			return fmt.Errorf("watcher: %w", err)
		}
		if err := w.WatchDir(s.appDir); err != nil {
			return fmt.Errorf("watching app root: %w", err)
		}
		s.mu.Lock()
		s.w = w
		s.mu.Unlock()
		s.syncWatchedDirs()
		w.Start()
		defer w.Close()
	}

	s.logger.Info("listening", "addr", "http://localhost"+addr)

	srv := &http.Server{
		Addr:              addr,
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	go func() {
		<-quit
		s.logger.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Production build
// ---------------------------------------------------------------------------

// BuildOptions configures the production build and static-site generation.
type BuildOptions struct {
	Logger *slog.Logger
	// GoLoaders and GoPaths are used by BuildStatic for programmatic (Go) loaders.
	// For CLI users the equivalent JS exports in *.loader.ts are used automatically.
	GoLoaders map[string]LoaderFunc
	GoPaths   map[string]PathsFunc
	Plugins   []esbuild.Plugin
}

// Build compiles all page bundles for appDir (minified, content-hash filenames),
// writes them to dist/_echo/bundle/, copies public/ to dist/public/, and writes
// dist/manifest.json so that `echo start` can serve without recompiling.
func Build(appDir string, opts ...BuildOptions) error {
	abs, err := filepath.Abs(appDir)
	if err != nil {
		return err
	}

	distDir := filepath.Join(abs, "dist")
	if err := os.RemoveAll(distDir); err != nil {
		return fmt.Errorf("cleaning dist/: %w", err)
	}

	logger := slog.Default()
	if len(opts) > 0 && opts[0].Logger != nil {
		logger = opts[0].Logger
	}
	var userPlugins []esbuild.Plugin
	if len(opts) > 0 {
		userPlugins = opts[0].Plugins
	}
	buildOpts := bundler.Options{
		AppDir:  abs,
		Minify:  true,
		Plugins: append(userPlugins, autoPlugins(abs, logger)...),
	}
	if lc := plugins.FindLightningCSS(abs); lc != "" {
		logger.Info("CSS: Lightning CSS enabled")
		buildOpts.CSSTransform = plugins.LightningCSSTransform(lc, true)
	}
	compiler, err := bundler.NewCompiler(buildOpts)
	if err != nil {
		return err
	}
	defer compiler.Close()

	pagesDir := filepath.Join(abs, "pages")
	routes, err := router.Scan(pagesDir)
	if err != nil {
		return fmt.Errorf("scanning pages: %w", err)
	}
	layoutMap, _ := layout.Find(pagesDir)

	bundleOutDir := filepath.Join(distDir, "_echo", "bundle")
	if err := os.MkdirAll(bundleOutDir, 0o755); err != nil {
		return fmt.Errorf("creating bundle dir: %w", err)
	}

	type distResult struct {
		route manifestRoute
		err   error
	}
	distResults := make([]distResult, len(routes))
	var wg sync.WaitGroup
	for i, r := range routes {
		wg.Add(1)
		go func(i int, r router.Route) {
			defer wg.Done()
			logger.Info("building", "key", r.BundleKey, "pattern", r.Pattern)
			b, err := compiler.Build(r.FilePath, layoutMap[r.BundleKey])
			if err != nil {
				distResults[i].err = fmt.Errorf("building %s: %w", r.FilePath, err)
				return
			}
			h := sha256.Sum256([]byte(b.JS + "\x00" + b.CSS))
			id := fmt.Sprintf("%x", h[:8])
			if err := os.WriteFile(filepath.Join(bundleOutDir, id+".js"), []byte(b.JS), 0o644); err != nil {
				distResults[i].err = fmt.Errorf("writing bundle: %w", err)
				return
			}
			logger.Info("wrote bundle", "file", id+".js")
			if b.CSS != "" {
				if err := os.WriteFile(filepath.Join(bundleOutDir, id+".css"), []byte(b.CSS), 0o644); err != nil {
					distResults[i].err = fmt.Errorf("writing css bundle: %w", err)
					return
				}
				logger.Info("wrote bundle", "file", id+".css")
			}
			title, description := readPageMeta(r.FilePath, r.Pattern)
			distResults[i].route = manifestRoute{
				Pattern:     r.Pattern,
				BundleKey:   r.BundleKey,
				BundleID:    id,
				HasCSS:      b.CSS != "",
				Title:       title,
				Description: description,
			}
		}(i, r)
	}
	wg.Wait()

	m := manifest{Routes: make([]manifestRoute, 0, len(routes))}
	for _, res := range distResults {
		if res.err != nil {
			return res.err
		}
		m.Routes = append(m.Routes, res.route)
	}

	manifestData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "manifest.json"), manifestData, 0o644); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	logger.Info("wrote manifest", "file", "dist/manifest.json")

	srcPublic := filepath.Join(abs, "public")
	if info, err := os.Stat(srcPublic); err == nil && info.IsDir() {
		dstPublic := filepath.Join(distDir, "public")
		if err := copyDir(srcPublic, dstPublic); err != nil {
			return fmt.Errorf("copying public/: %w", err)
		}
		logger.Info("copied public dir", "dst", "dist/public/")
	}

	return nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request, page compiledPage, status int) {
	shell := page.shell

	if fn, ok := s.goLoaders[page.route.Pattern]; ok {
		data, err := fn(r)
		if err != nil {
			s.logger.Error("loader error", "pattern", page.route.Pattern, "err", err)
			s.handleError(w, r, http.StatusInternalServerError, err)
			return
		}
		jsonBytes, err := json.Marshal(data)
		if err != nil {
			s.logger.Error("loader encode error", "pattern", page.route.Pattern, "err", err)
			s.handleError(w, r, http.StatusInternalServerError, err)
			return
		}
		shell = injectLoaderData(shell, jsonBytes)
	} else {
		s.mu.RLock()
		jsLoader := s.jsLoaders[page.route.BundleKey]
		s.mu.RUnlock()
		if jsLoader != nil {
			data, err := jsLoader.Run(loaderContextFromRequest(page.route.Pattern, r))
			if err != nil {
				s.logger.Error("loader error", "pattern", page.route.Pattern, "err", err)
				s.handleError(w, r, http.StatusInternalServerError, err)
				return
			}
			shell = injectLoaderData(shell, data)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, shell)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------
type ErrorContext struct {
	Message string `json:"message"`
	Path    string `json:"path"`
	Status  int    `json:"status"`
}

func (s *Server) handleError(w http.ResponseWriter, r *http.Request, status int, err error) {
	s.mu.RLock()
	pages := s.pages
	s.mu.RUnlock()

	var errorPage *compiledPage
	for i := range pages {
		if pages[i].route.BundleKey == "500" {
			cp := pages[i]
			errorPage = &cp
			break
		}
	}

	if errorPage == nil {
		http.Error(w, http.StatusText(status), status)
		return
	}

	data, _ := json.Marshal(ErrorContext{
		Message: err.Error(),
		Path:    r.URL.Path,
		Status:  status,
	})
	shell := injectLoaderData(errorPage.shell, data)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, shell)
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				var err error
				switch v := rec.(type) {
				case error:
					err = v
				default:
					err = fmt.Errorf("%v", v)
				}
				s.logger.Error("panic recovered", "err", err, "path", r.URL.Path)
				s.handleError(w, r, http.StatusInternalServerError, err)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const maxAPIBodyBytes = 4 << 20 // 4 MB

func (s *Server) handleAPIRunner(w http.ResponseWriter, r *http.Request, pattern string, run *loader.APIRunner) {
	body := ""
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxAPIBodyBytes)
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		body = string(b)
	}
	req := loader.APIRequest{
		Method:       r.Method,
		Params:       extractPathParams(pattern, r),
		SearchParams: queryToMap(r.URL.Query()),
		Headers:      headerToMap(r.Header),
		Body:         body,
	}
	resp, err := run.Run(req)
	if err != nil {
		s.logger.Error("api handler error", "pattern", pattern, "err", err)
		http.Error(w, "api handler error", http.StatusInternalServerError)
		return
	}
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.Status)
	if resp.Body != nil {
		_, _ = w.Write(resp.Body)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"status":"ok","version":%q}`, version)
}

func (s *Server) handleBundle(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/_echo/bundle/")

	s.mu.RLock()
	content, ok := s.bundles[name]
	s.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	contentType := "text/javascript; charset=utf-8"
	if strings.HasSuffix(name, ".css") {
		contentType = "text/css; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, content)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	_, _ = fmt.Fprintf(w, "data: connected\n\n")
	flusher.Flush()

	ch := make(chan string, 2)
	s.sseClients.Store(ch, struct{}{})
	defer s.sseClients.Delete(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-ch:
			_, _ = fmt.Fprint(w, event)
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// SSE
// ---------------------------------------------------------------------------

func (s *Server) broadcastReload() {
	s.broadcastSSE("data: reload\n\n")
}

func (s *Server) broadcastError(msg string) {
	payload, _ := json.Marshal(map[string]string{"message": msg})
	s.broadcastSSE("event: build_error\ndata: " + string(payload) + "\n\n")
}

func (s *Server) broadcastSSE(event string) {
	s.sseClients.Range(func(k, _ any) bool {
		ch := k.(chan string)
		select {
		case ch <- event:
		default:
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func injectLoaderData(shell string, data json.RawMessage) string {
	script := `<script id="__echo_data__" type="application/json">` + string(data) + `</script>`
	return strings.Replace(shell, "</body>", script+"\n</body>", 1)
}

func loaderContextFromRequest(pattern string, r *http.Request) loader.Context {
	return loader.Context{
		Params:       extractPathParams(pattern, r),
		SearchParams: queryToMap(r.URL.Query()),
		Headers:      headerToMap(r.Header),
	}
}

func extractPathParams(pattern string, r *http.Request) map[string]string {
	params := make(map[string]string)
	for seg := range strings.SplitSeq(pattern, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.TrimSuffix(seg[1:len(seg)-1], "...")
			if name != "" {
				params[name] = r.PathValue(name)
			}
		}
	}
	return params
}

func queryToMap(q map[string][]string) map[string]string {
	m := make(map[string]string, len(q))
	for k := range q {
		m[k] = q[k][0]
	}
	return m
}

func headerToMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k := range h {
		m[k] = h.Get(k)
	}
	return m
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g gzipResponseWriter) Write(b []byte) (int, error) {
	return g.gz.Write(b)
}

func headersMiddleware(headers map[string]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") || r.URL.Path == "/_echo/sse" {
			next.ServeHTTP(w, r)
			return
		}
		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer gz.Close()
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Del("Content-Length")
		next.ServeHTTP(gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

func titleFromPattern(pattern string) string {
	if pattern == "/" {
		return "Home"
	}
	parts := strings.Split(strings.Trim(pattern, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		seg := parts[i]
		if seg == "" || strings.HasPrefix(seg, "{") {
			continue
		}
		return strings.ToUpper(seg[:1]) + seg[1:]
	}
	return "Echo"
}

func routesFromPages(pages []compiledPage) []router.Route {
	routes := make([]router.Route, len(pages))
	for i, p := range pages {
		routes[i] = p.route
	}
	return routes
}

func (s *Server) disposeRemovedContexts(oldRoutes, newRoutes []router.Route) {
	if s.compiler == nil {
		return
	}
	next := make(map[string]struct{}, len(newRoutes))
	for _, r := range newRoutes {
		next[r.FilePath] = struct{}{}
	}
	for _, r := range oldRoutes {
		if _, ok := next[r.FilePath]; !ok {
			s.compiler.Remove(r.FilePath)
		}
	}
}

func routesChanged(prev, next []router.Route) bool {
	if len(prev) != len(next) {
		return true
	}
	for i := range prev {
		if prev[i].FilePath != next[i].FilePath ||
			prev[i].Pattern != next[i].Pattern ||
			prev[i].BundleKey != next[i].BundleKey {
			return true
		}
	}
	return false
}

func (s *Server) normalizePaths(paths []string) map[string]struct{} {
	normalized := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(s.appDir, p)
		}
		normalized[filepath.ToSlash(filepath.Clean(p))] = struct{}{}
	}
	return normalized
}

func affectedBundleKeys(routes []router.Route, inputs map[string]map[string]struct{}, changed map[string]struct{}) map[string]struct{} {
	affected := make(map[string]struct{})
	for _, route := range routes {
		routePath := filepath.ToSlash(filepath.Clean(route.FilePath))
		if _, ok := changed[routePath]; ok {
			affected[route.BundleKey] = struct{}{}
			continue
		}
		metaPath := filepath.ToSlash(strings.TrimSuffix(routePath, filepath.Ext(routePath)) + ".meta.json")
		if _, ok := changed[metaPath]; ok {
			affected[route.BundleKey] = struct{}{}
			continue
		}
		for dep := range inputs[route.BundleKey] {
			if _, ok := changed[dep]; ok {
				affected[route.BundleKey] = struct{}{}
				break
			}
		}
	}
	return affected
}

func readPageMeta(pageFilePath, pattern string) (title, description string) {
	sidecar := strings.TrimSuffix(pageFilePath, filepath.Ext(pageFilePath)) + ".meta.json"
	if data, err := os.ReadFile(sidecar); err == nil {
		var raw struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if json.Unmarshal(data, &raw) == nil {
			title = raw.Title
			description = raw.Description
		}
	}
	if title == "" {
		title = titleFromPattern(pattern)
	}
	return
}

func (s *Server) syncWatchedDirs() {
	s.mu.RLock()
	w := s.w
	inputs := s.inputs
	s.mu.RUnlock()
	if w == nil {
		return
	}

	seen := make(map[string]struct{})
	for _, fileSet := range inputs {
		for filePath := range fileSet {
			seen[filepath.Dir(filepath.FromSlash(filePath))] = struct{}{}
		}
	}

	dirs := make([]string, 0, len(seen))
	for dir := range seen {
		dirs = append(dirs, dir)
	}

	if err := w.SyncDirs(dirs); err != nil {
		s.logger.Warn("watcher sync error", "err", err)
	}
}

func toSet(values []string) map[string]struct{} {
	s := make(map[string]struct{}, len(values))
	for _, v := range values {
		s[v] = struct{}{}
	}
	return s
}

func cloneStringMap(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

func cloneInputSetMap(src map[string]map[string]struct{}) map[string]map[string]struct{} {
	dst := make(map[string]map[string]struct{}, len(src))
	for k, set := range src {
		inner := make(map[string]struct{}, len(set))
		for v := range set {
			inner[v] = struct{}{}
		}
		dst[k] = inner
	}
	return dst
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(srcPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, srcPath)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}
