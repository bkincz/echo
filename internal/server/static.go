package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/echo-ssr/echo/internal/layout"
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

	logger := slog.Default()
	var goLoaders map[string]LoaderFunc
	var goPaths map[string]PathsFunc
	if len(opts) > 0 {
		if opts[0].Logger != nil {
			logger = opts[0].Logger
		}
		goLoaders = opts[0].GoLoaders
		goPaths = opts[0].GoPaths
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

	pagesDir := filepath.Join(abs, "pages")
	loaderFiles, _ := loader.Find(pagesDir)
	jsLoaders := make(map[string]*loader.Loader, len(loaderFiles))
	for key, filePath := range loaderFiles {
		l, err := loader.Build(abs, filePath)
		if err != nil {
			logger.Warn("static build: skipping loader", "key", key, "err", err)
			continue
		}
		defer l.Close()
		jsLoaders[key] = l
	}

	layoutMap, _ := layout.Find(pagesDir)
	_ = layoutMap

	logger.Info("static build: generating HTML")
	for _, mr := range m.Routes {
		pathEntries, err := resolvePathEntries(mr, jsLoaders, goLoaders, goPaths, logger)
		if err != nil {
			return err
		}
		for _, params := range pathEntries {
			if err := writeStaticPage(abs, distDir, mr, params, jsLoaders, goLoaders, logger); err != nil {
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
	appDir, distDir string,
	mr manifestRoute,
	params map[string]string,
	jsLoaders map[string]*loader.Loader,
	goLoaders map[string]LoaderFunc,
	logger *slog.Logger,
) error {
	cssBundleURL := ""
	if mr.HasCSS {
		cssBundleURL = "/_echo/bundle/" + mr.BundleID + ".css"
	}
	shell, err := renderer.Shell(renderer.ShellOptions{
		Title:        mr.Title,
		Description:  mr.Description,
		BundleURL:    "/_echo/bundle/" + mr.BundleID + ".js",
		CSSBundleURL: cssBundleURL,
	})
	if err != nil {
		return fmt.Errorf("rendering shell for %s: %w", mr.Pattern, err)
	}

	// Call loader.
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
	if data != nil {
		shell = injectLoaderData(shell, data)
	}

	// Determine output file path
	outFile := patternToFilePath(mr.Pattern, params)
	fullPath := filepath.Join(distDir, filepath.FromSlash(outFile))

	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("creating dir for %s: %w", outFile, err)
	}
	if err := os.WriteFile(fullPath, []byte(shell), 0o644); err != nil {
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
	p := pattern
	for name, val := range params {
		p = strings.ReplaceAll(p, "{"+name+"...}", val)
		p = strings.ReplaceAll(p, "{"+name+"}", val)
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "index.html"
	}
	return p + "/index.html"
}

func syntheticRequest(pattern string, params map[string]string) *http.Request {
	p := pattern
	for name, val := range params {
		p = strings.ReplaceAll(p, "{"+name+"...}", val)
		p = strings.ReplaceAll(p, "{"+name+"}", val)
	}
	req, _ := http.NewRequest(http.MethodGet, p, nil)
	for name, val := range params {
		req.SetPathValue(name, val)
	}
	return req
}
