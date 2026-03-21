package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	t.Parallel()
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.Port != 3000 {
		t.Errorf("port = %d, want 3000", cfg.Port)
	}
	if cfg.JS.LoaderTimeoutMs != 10000 || cfg.JS.APITimeoutMs != 10000 || cfg.JS.PathsTimeoutMs != 10000 {
		t.Errorf("unexpected default js timeouts: %+v", cfg.JS)
	}
}

func TestLoadValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "echo.config.json"), `{
		"port": 8080,
		"headers": {"X-Frame-Options": "DENY"},
		"js": {
			"loaderTimeoutMs": 2500,
			"apiTimeoutMs": 7000,
			"pathsTimeoutMs": 9000
		}
	}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Port)
	}
	if cfg.Headers["X-Frame-Options"] != "DENY" {
		t.Errorf("header X-Frame-Options = %q, want DENY", cfg.Headers["X-Frame-Options"])
	}
	if cfg.JS.LoaderTimeoutMs != 2500 || cfg.JS.APITimeoutMs != 7000 || cfg.JS.PathsTimeoutMs != 9000 {
		t.Errorf("unexpected js timeouts: %+v", cfg.JS)
	}
}

func TestLoadPartialJSTimeoutsUseDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "echo.config.json"), `{
		"js": {
			"loaderTimeoutMs": 1500
		}
	}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.JS.LoaderTimeoutMs != 1500 {
		t.Errorf("loaderTimeoutMs = %d, want 1500", cfg.JS.LoaderTimeoutMs)
	}
	if cfg.JS.APITimeoutMs != 10000 {
		t.Errorf("apiTimeoutMs = %d, want 10000", cfg.JS.APITimeoutMs)
	}
	if cfg.JS.PathsTimeoutMs != 10000 {
		t.Errorf("pathsTimeoutMs = %d, want 10000", cfg.JS.PathsTimeoutMs)
	}
}

func TestLoadNonPositiveTimeoutsResetToDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "echo.config.json"), `{
		"js": {
			"loaderTimeoutMs": 0,
			"apiTimeoutMs": -1,
			"pathsTimeoutMs": 0
		}
	}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.JS.LoaderTimeoutMs != 10000 || cfg.JS.APITimeoutMs != 10000 || cfg.JS.PathsTimeoutMs != 10000 {
		t.Errorf("expected defaults for non-positive values, got %+v", cfg.JS)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "echo.config.json"), `{invalid}`)

	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func writeJSON(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
