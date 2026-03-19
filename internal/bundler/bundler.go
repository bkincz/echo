package bundler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Options struct {
	AppDir       string
	Minify       bool
	CSSTransform func(css string) (string, error)
	Plugins      []api.Plugin
}

type Bundle struct {
	JS     string
	CSS    string
	Inputs []string
}

type cachedBuild struct {
	ctx api.BuildContext
}

type Compiler struct {
	opts            Options
	clientEntryPath string

	mu    sync.Mutex
	cache map[string]cachedBuild
}

// ---------------------------------------------------------------------------
// Compiler
// ---------------------------------------------------------------------------
func NewCompiler(opts Options) (*Compiler, error) {
	entry, err := findClientEntry(opts.AppDir)
	if err != nil {
		return nil, err
	}
	return &Compiler{
		opts:            opts,
		clientEntryPath: entry,
		cache:           make(map[string]cachedBuild),
	}, nil
}

func (c *Compiler) Build(pageFilePath string, layouts []string) (*Bundle, error) {
	pageFilePath = filepath.ToSlash(filepath.Clean(pageFilePath))

	c.mu.Lock()
	cached, ok := c.cache[pageFilePath]
	if !ok {
		ctx, err := newContext(pageFilePath, c.clientEntryPath, layouts, c.opts)
		if err != nil {
			c.mu.Unlock()
			return nil, err
		}
		cached = cachedBuild{ctx: ctx}
		c.cache[pageFilePath] = cached
	}
	ctx := cached.ctx
	c.mu.Unlock()

	result := ctx.Rebuild()
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("esbuild: %s", joinMessages(result.Errors))
	}
	var js, css string
	for _, f := range result.OutputFiles {
		switch filepath.Ext(f.Path) {
		case ".js", ".mjs":
			js = string(f.Contents)
		case ".css":
			css = string(f.Contents)
		}
	}
	if js == "" {
		return nil, fmt.Errorf("esbuild produced no JS output for %s", pageFilePath)
	}

	if css != "" && c.opts.CSSTransform != nil {
		if out, err := c.opts.CSSTransform(css); err == nil {
			css = out
		} else {
			slog.Default().Warn("CSS post-process error, using esbuild output", "err", err)
		}
	}

	inputs, err := parseInputs(result.Metafile, c.opts.AppDir)
	if err != nil {
		return nil, err
	}

	return &Bundle{JS: js, CSS: css, Inputs: inputs}, nil
}

func (c *Compiler) Remove(pageFilePath string) {
	pageFilePath = filepath.ToSlash(filepath.Clean(pageFilePath))

	c.mu.Lock()
	cached, ok := c.cache[pageFilePath]
	if ok {
		delete(c.cache, pageFilePath)
	}
	c.mu.Unlock()

	if ok {
		cached.ctx.Dispose()
	}
}

func (c *Compiler) Close() {
	c.mu.Lock()
	cached := c.cache
	c.cache = make(map[string]cachedBuild)
	c.mu.Unlock()

	for _, b := range cached {
		b.ctx.Dispose()
	}
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

func newContext(pageFilePath, clientEntryPath string, layouts []string, opts Options) (api.BuildContext, error) {
	var sb strings.Builder
	for i, lp := range layouts {
		fmt.Fprintf(&sb, "import * as _layout%d from %q;\n", i, lp)
	}
	var layoutArr strings.Builder
	layoutArr.WriteString("[")
	for i := range layouts {
		if i > 0 {
			layoutArr.WriteString(", ")
		}
		fmt.Fprintf(&layoutArr, "_layout%d", i)
	}
	layoutArr.WriteString("]")

	entry := fmt.Sprintf(`%simport * as PageModule from %q;
import { mount } from %q;
const __echoRoot = document.getElementById("root");
if (__echoRoot) mount(__echoRoot, %s, PageModule);
`, sb.String(), pageFilePath, clientEntryPath, layoutArr.String())

	buildOpts := api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry,
			ResolveDir: opts.AppDir,
			Loader:     api.LoaderTSX,
		},
		Bundle:    true,
		Platform:  api.PlatformBrowser,
		Format:    api.FormatESModule,
		JSX:       api.JSXAutomatic,
		Target:    api.ES2020,
		Sourcemap: api.SourceMapInline,
		Metafile:  true,
		Write:     false,
		Outdir:    opts.AppDir,
		Loader: map[string]api.Loader{
			".module.css": api.LoaderLocalCSS,
		},
		Plugins: opts.Plugins,
	}

	if opts.Minify {
		buildOpts.MinifyWhitespace = true
		buildOpts.MinifySyntax = true
		buildOpts.MinifyIdentifiers = true
		buildOpts.Sourcemap = api.SourceMapNone
	}

	ctx, ctxErr := api.Context(buildOpts)
	if ctxErr != nil {
		return nil, fmt.Errorf("esbuild context: %s", joinMessages(ctxErr.Errors))
	}
	return ctx, nil
}

func joinMessages(msgs []api.Message) string {
	if len(msgs) == 0 {
		return "unknown esbuild error"
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Text)
	}
	return strings.Join(parts, "; ")
}

func parseInputs(metaJSON string, appDir string) ([]string, error) {
	if metaJSON == "" {
		return nil, nil
	}

	var meta struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return nil, fmt.Errorf("parse esbuild metafile: %w", err)
	}

	seen := make(map[string]struct{}, len(meta.Inputs))
	for input := range meta.Inputs {
		if input == "<stdin>" {
			continue
		}
		normalized := input
		if !filepath.IsAbs(normalized) {
			normalized = filepath.Join(appDir, normalized)
		}
		normalized = filepath.ToSlash(filepath.Clean(normalized))
		seen[normalized] = struct{}{}
	}

	inputs := make([]string, 0, len(seen))
	for input := range seen {
		inputs = append(inputs, input)
	}
	sort.Strings(inputs)
	return inputs, nil
}

func findClientEntry(appDir string) (string, error) {
	candidates := []string{
		"client.tsx",
		"client.ts",
		"client.jsx",
		"client.js",
	}
	for _, rel := range candidates {
		abs := filepath.Join(appDir, rel)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("checking client adapter %s: %w", abs, err)
		}
		if !info.IsDir() {
			return filepath.ToSlash(abs), nil
		}
	}
	return "", fmt.Errorf(
		"missing client adapter — add one of: %s",
		strings.Join(candidates, ", "),
	)
}
