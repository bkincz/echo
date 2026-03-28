package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Config struct {
	Port     int               `json:"port"`
	BasePath string            `json:"basePath"`
	Headers  map[string]string `json:"headers"`
	Paths    PathsConfig       `json:"paths"`
	Frontend FrontendConfig    `json:"frontend"`
	JS       JSTimeouts        `json:"js"`
}

type PathsConfig struct {
	PagesDir  string `json:"pagesDir"`
	PublicDir string `json:"publicDir"`
}

type FrontendConfig struct {
	ClientEntry string `json:"clientEntry"`
	SSREntry    string `json:"ssrEntry"`
}

type JSTimeouts struct {
	LoaderTimeoutMs int `json:"loaderTimeoutMs"`
	APITimeoutMs    int `json:"apiTimeoutMs"`
	PathsTimeoutMs  int `json:"pathsTimeoutMs"`
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func Load(appDir string) (Config, error) {
	tsPath := filepath.Join(appDir, "echo.config.ts")
	if _, err := os.Stat(tsPath); err == nil {
		return loadTS(tsPath)
	}
	return loadJSON(filepath.Join(appDir, "echo.config.json"))
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

func loadJSON(path string) (Config, error) {
	c := Defaults()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	c.applyDefaults()
	return c, nil
}

func loadTS(tsPath string) (Config, error) {
	src, err := os.ReadFile(tsPath)
	if err != nil {
		return Config{}, err
	}

	tr := esbuild.Transform(string(src), esbuild.TransformOptions{
		Loader:   esbuild.LoaderTS,
		Platform: esbuild.PlatformNode,
		Format:   esbuild.FormatCommonJS,
	})
	if len(tr.Errors) > 0 {
		return Config{}, fmt.Errorf("compiling echo.config.ts: %s", tr.Errors[0].Text)
	}

	tmp, err := os.CreateTemp("", "echo-config-*.cjs")
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(tr.Code); err != nil {
		tmp.Close()
		return Config{}, err
	}
	tmp.Close()

	rt, err := jsruntime.Require()
	if err != nil {
		return Config{}, fmt.Errorf("echo.config.ts: %w", err)
	}
	const script = `const c = require(process.argv[1]); process.stdout.write(JSON.stringify(c.default ?? c));`
	out, err := exec.Command(rt, "-e", script, tmp.Name()).Output() //nolint:gosec
	if err != nil {
		return Config{}, fmt.Errorf("evaluating echo.config.ts: %w", err)
	}

	c := Defaults()
	if err := json.Unmarshal(out, &c); err != nil {
		return Config{}, fmt.Errorf("parsing echo.config.ts output: %w", err)
	}
	c.applyDefaults()
	return c, nil
}

func Defaults() Config {
	return Config{
		Port:    3000,
		Headers: map[string]string{},
		Paths: PathsConfig{
			PagesDir:  "pages",
			PublicDir: "public",
		},
		Frontend: FrontendConfig{},
		JS: JSTimeouts{
			LoaderTimeoutMs: 10000,
			APITimeoutMs:    10000,
			PathsTimeoutMs:  10000,
		},
	}
}

func (c *Config) applyDefaults() {
	def := Defaults()
	if c.Port <= 0 {
		c.Port = def.Port
	}
	c.BasePath = NormalizeBasePath(c.BasePath)
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	if c.Paths.PagesDir == "" {
		c.Paths.PagesDir = def.Paths.PagesDir
	}
	if c.Paths.PublicDir == "" {
		c.Paths.PublicDir = def.Paths.PublicDir
	}
	if c.JS.LoaderTimeoutMs <= 0 {
		c.JS.LoaderTimeoutMs = def.JS.LoaderTimeoutMs
	}
	if c.JS.APITimeoutMs <= 0 {
		c.JS.APITimeoutMs = def.JS.APITimeoutMs
	}
	if c.JS.PathsTimeoutMs <= 0 {
		c.JS.PathsTimeoutMs = def.JS.PathsTimeoutMs
	}
}

func NormalizeBasePath(basePath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		return ""
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	cleaned := path.Clean(basePath)
	if cleaned == "." || cleaned == "/" {
		return ""
	}
	return cleaned
}
