package loader

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// ---------------------------------------------------------------------------
// Find
// ---------------------------------------------------------------------------
func TestFind(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustTouch(t, filepath.Join(pagesDir, "index.tsx"))
	mustTouch(t, filepath.Join(pagesDir, "index.loader.ts"))
	mustTouch(t, filepath.Join(pagesDir, "about.tsx"))
	mustTouch(t, filepath.Join(pagesDir, "blog", "[id].tsx"))
	mustTouch(t, filepath.Join(pagesDir, "blog", "[id].loader.ts"))

	got, err := Find(pagesDir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	want := map[string]bool{
		"index":     true,
		"blog/[id]": true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d loaders, want %d: %v", len(got), len(want), got)
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("missing loader key %q", key)
		}
	}
}

func TestFindEmpty(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustTouch(t, filepath.Join(pagesDir, "index.tsx"))

	got, err := Find(pagesDir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no loaders, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// IsLoaderFile
// ---------------------------------------------------------------------------
func TestIsLoaderFile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{"pages/index.loader.ts", true},
		{"pages/index.loader.tsx", true},
		{"pages/index.loader.js", true},
		{"pages/index.loader.jsx", true},
		{"pages/index.tsx", false},
		{"pages/index.ts", false},
		{"pages/loader.ts", false},
	}
	for _, tc := range cases {
		if got := IsLoaderFile(tc.path); got != tc.want {
			t.Errorf("IsLoaderFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// findJSRuntime
// ---------------------------------------------------------------------------
func TestFindJSRuntime(t *testing.T) {
	t.Parallel()

	rt := jsruntime.Find()
	if rt != "" && rt != "node" {
		t.Errorf("jsruntime.Find() = %q, want node or empty", rt)
	}

	if rt == "node" {
		if _, err := exec.LookPath(rt); err != nil {
			t.Errorf("jsruntime.Find() returned %q but it is not in PATH: %v", rt, err)
		}
	}
}

func TestRunnerTimeouts(t *testing.T) {
	t.Parallel()

	if _, err := jsruntime.Require(); err != nil {
		t.Skipf("node runtime unavailable: %v", err)
	}

	appDir := t.TempDir()
	loaderPath := filepath.Join(appDir, "pages", "slow.loader.ts")
	if err := os.MkdirAll(filepath.Dir(loaderPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(loaderPath, []byte(`export async function loader() { return await new Promise((resolve) => setTimeout(() => resolve({ ok: true }), 3000)); }`), 0o644); err != nil {
		t.Fatalf("write loader: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "pages", "slow.tsx"), []byte("export default function Page(){return null;}"), 0o644); err != nil {
		t.Fatalf("write page: %v", err)
	}

	l, err := Build(appDir, loaderPath)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = l.RunWithContext(ctx, Context{})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAPIRunnerTimeout(t *testing.T) {
	t.Parallel()

	if _, err := jsruntime.Require(); err != nil {
		t.Skipf("node runtime unavailable: %v", err)
	}

	appDir := t.TempDir()
	apiPath := filepath.Join(appDir, "pages", "api", "slow.ts")
	if err := os.MkdirAll(filepath.Dir(apiPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(apiPath, []byte(`export async function handler() { return await new Promise((resolve) => setTimeout(() => resolve({ status: 200, body: { ok: true } }), 3000)); }`), 0o644); err != nil {
		t.Fatalf("write api file: %v", err)
	}

	runner, err := BuildAPI(appDir, apiPath)
	if err != nil {
		t.Fatalf("BuildAPI: %v", err)
	}
	defer runner.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = runner.RunWithContext(ctx, APIRequest{Method: "GET"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPathsTimeout(t *testing.T) {
	t.Parallel()

	if _, err := jsruntime.Require(); err != nil {
		t.Skipf("node runtime unavailable: %v", err)
	}

	appDir := t.TempDir()
	loaderPath := filepath.Join(appDir, "pages", "slow.loader.ts")
	if err := os.MkdirAll(filepath.Dir(loaderPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(loaderPath, []byte(`export async function paths() { return await new Promise((resolve) => setTimeout(() => resolve([{ id: "one" }]), 3000)); }`), 0o644); err != nil {
		t.Fatalf("write loader: %v", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "pages", "slow.tsx"), []byte("export default function Page(){return null;}"), 0o644); err != nil {
		t.Fatalf("write page: %v", err)
	}

	l, err := Build(appDir, loaderPath)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = l.PathsWithContext(ctx)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
func mustTouch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
