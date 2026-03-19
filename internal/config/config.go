package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	esbuild "github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------
type Config struct {
	Port    string            `json:"port"`
	Headers map[string]string `json:"headers"`
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
	out, err := exec.Command(rt, "-e", script, tmp.Name()).Output()
	if err != nil {
		return Config{}, fmt.Errorf("evaluating echo.config.ts: %w", err)
	}

	c := Defaults()
	if err := json.Unmarshal(out, &c); err != nil {
		return Config{}, fmt.Errorf("parsing echo.config.ts output: %w", err)
	}
	if c.Port == "" {
		c.Port = Defaults().Port
	}
	return c, nil
}

func Defaults() Config {
	return Config{
		Port:    "3000",
		Headers: map[string]string{},
	}
}
