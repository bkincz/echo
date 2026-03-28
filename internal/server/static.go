package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/echo-ssr/echo/internal/config"
	"github.com/echo-ssr/echo/internal/frontend"
	"github.com/echo-ssr/echo/internal/loader"
	"github.com/echo-ssr/echo/internal/renderer"
)

// ---------------------------------------------------------------------------
// Static build
// ---------------------------------------------------------------------------
func BuildStatic(appDir string, opts ...BuildOptions) error {
	abs, err := filepath.Abs(appDir)
	if err != nil {
		return err
	}
	cfg, err := config.Load(abs)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger := slog.Default()
	buildOpts := BuildOptions{}
	var goLoaders map[string]LoaderFunc
	var goPaths map[string]PathsFunc
	if len(opts) > 0 {
		buildOpts = opts[0]
		if buildOpts.Logger != nil {
			logger = buildOpts.Logger
		}
		goLoaders = buildOpts.GoLoaders
		goPaths = buildOpts.GoPaths
	}

	logger.Info("static build: compiling bundles")
	if err := Build(abs, opts...); err != nil {
		return err
	}

	distDir := filepath.Join(abs, "dist")
	manifestData, err := os.ReadFile(filepath.Join(distDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return fmt.Errorf("parsing manifest: %w", err)
	}
	if m.Frontend == nil || m.Frontend.SSREntry == "" {
		return fmt.Errorf("manifest missing frontend SSR metadata")
	}

	engine := resolveFrontendEngine(buildOpts.Frontend, logger, 0)
	if engine == nil {
		return fmt.Errorf("frontend engine unavailable for static SSR rendering")
	}
	defer func() {
		if err := engine.Close(); err != nil {
			logger.Warn("static build: frontend shutdown", "engine", engine.Name(), "err", err)
		}
	}()

	pagesDir := filepath.Join(abs, filepath.FromSlash(cfg.Paths.PagesDir))
	loaderFiles, _ := loader.Find(pagesDir)
	jsLoaders := make(map[string]*loader.Loader, len(loaderFiles))
	runnerOpts := loaderRunnerOptionsFromConfig(cfg)
	for key, filePath := range loaderFiles {
		l, err := loader.BuildWithOptions(abs, filePath, runnerOpts)
		if err != nil {
			logger.Warn("static build: skipping loader", "key", key, "err", err)
			continue
		}
		defer l.Close()
		jsLoaders[key] = l
	}

	logger.Info("static build: generating HTML")
	for _, mr := range m.Routes {
		pathEntries, err := resolvePathEntries(mr, jsLoaders, goLoaders, goPaths, logger)
		if err != nil {
			return err
		}
		for _, params := range pathEntries {
			if err := writeStaticPage(abs, distDir, cfg.BasePath, mr, params, jsLoaders, goLoaders, engine, m.Frontend.SSREntry, logger); err != nil {
				return err
			}
		}
	}

	logger.Info("static build complete", "dir", "dist/")
	return nil
}

func resolvePathEntries(
	mr manifestRoute,
	jsLoaders map[string]*loader.Loader,
	goLoaders map[string]LoaderFunc,
	goPaths map[string]PathsFunc,
	logger *slog.Logger,
) ([]map[string]string, error) {
	if !isDynamicPattern(mr.Pattern) {
		return []map[string]string{{}}, nil
	}

	if goPaths != nil {
		if fn, ok := goPaths[mr.Pattern]; ok {
			paths, err := fn()
			if err != nil {
				return nil, fmt.Errorf("paths() for %s: %w", mr.Pattern, err)
			}
			return paths, nil
		}
	}

	if l, ok := jsLoaders[mr.BundleKey]; ok {
		paths, err := l.Paths()
		if err != nil {
			return nil, fmt.Errorf("paths() for %s: %w", mr.Pattern, err)
		}
		if len(paths) > 0 {
			return paths, nil
		}
	}

	logger.Warn("static build: dynamic route has no paths(), skipping", "pattern", mr.Pattern)
	return nil, nil
}

func writeStaticPage(
	appDir, distDir, basePath string,
	mr manifestRoute,
	params map[string]string,
	jsLoaders map[string]*loader.Loader,
	goLoaders map[string]LoaderFunc,
	engine frontend.Engine,
	ssrEntry string,
	logger *slog.Logger,
) error {
	cssBundleURL := ""
	if mr.HasCSS {
		cssBundleURL = prefixURLPath(basePath, "/_echo/bundle/"+mr.BundleID+".css")
	}
	shell, err := renderer.Shell(renderer.ShellOptions{
		Title:        mr.Title,
		Description:  mr.Description,
		BundleURL:    prefixURLPath(basePath, "/_echo/bundle/"+mr.BundleID+".js"),
		CSSBundleURL: cssBundleURL,
		SSEURL:       prefixURLPath(basePath, "/_echo/sse"),
	})
	if err != nil {
		return fmt.Errorf("rendering shell for %s: %w", mr.Pattern, err)
	}

	var data json.RawMessage
	if goLoaders != nil {
		if fn, ok := goLoaders[mr.Pattern]; ok {
			req := syntheticRequest(mr.Pattern, params)
			result, err := fn(req)
			if err != nil {
				return fmt.Errorf("loader for %s: %w", mr.Pattern, err)
			}
			data, _ = json.Marshal(result)
		}
	}
	if data == nil {
		if l, ok := jsLoaders[mr.BundleKey]; ok {
			ctx := loader.Context{
				Params:       params,
				SearchParams: map[string]string{},
				Headers:      map[string]string{},
			}
			data, err = l.Run(ctx)
			if err != nil {
				logger.Warn("static build: loader error, writing shell without data",
					"pattern", mr.Pattern, "err", err)
			}
		}
	}
	urlPath := concretePatternPath(mr.Pattern, params)
	rendered, err := engine.Render(context.Background(), appDir, frontend.RenderOptions{
		SSREntry:     ssrEntry,
		URL:          urlPath,
		RoutePattern: mr.Pattern,
		Status:       http.StatusOK,
		Shell:        shell,
		LoaderData:   data,
	})
	if err != nil {
		return fmt.Errorf("rendering static page %s: %w", mr.Pattern, err)
	}

	outFile := patternToFilePath(mr.Pattern, params)
	fullPath, err := safeJoinUnder(distDir, outFile)
	if err != nil {
		return fmt.Errorf("resolving static output path for %s: %w", outFile, err)
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", outFile, err)
	}
	if err := os.WriteFile(fullPath, []byte(rendered.HTML), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outFile, err)
	}
	logger.Info("wrote", "file", outFile)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func isDynamicPattern(pattern string) bool {
	return strings.Contains(pattern, "{")
}

func patternToFilePath(pattern string, params map[string]string) string {
	p := concretePatternPath(pattern, params)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "index.html"
	}
	return p + "/index.html"
}

func syntheticRequest(pattern string, params map[string]string) *http.Request {
	p := concretePatternPath(pattern, params)
	req, _ := http.NewRequest(http.MethodGet, p, nil)
	for name, val := range params {
		req.SetPathValue(name, val)
	}
	return req
}

func concretePatternPath(pattern string, params map[string]string) string {
	p := pattern
	for name, val := range params {
		p = strings.ReplaceAll(p, "{"+name+"...}", val)
		p = strings.ReplaceAll(p, "{"+name+"}", val)
	}
	return p
}
