package loader

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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
	if rt != "bun" && rt != "node" {
		t.Errorf("jsruntime.Find() = %q, want bun or node", rt)
	}

	if rt != "node" {
		if _, err := exec.LookPath(rt); err != nil {
			t.Errorf("jsruntime.Find() returned %q but it is not in PATH: %v", rt, err)
		}
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
