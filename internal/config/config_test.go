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
	if cfg.Port != "3000" {
		t.Errorf("port = %q, want %q", cfg.Port, "3000")
	}
}

func TestLoadValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "echo.config.json"), `{
		"port": "8080",
		"headers": {"X-Frame-Options": "DENY"}
	}`)

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("port = %q, want %q", cfg.Port, "8080")
	}
	if cfg.Headers["X-Frame-Options"] != "DENY" {
		t.Errorf("header X-Frame-Options = %q, want DENY", cfg.Headers["X-Frame-Options"])
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
