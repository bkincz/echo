package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// fileToPattern
// ---------------------------------------------------------------------------
func TestFileToPattern(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		rel         string
		want        string
		errContains string
	}{
		{name: "root index", rel: "index.tsx", want: "/"},
		{name: "about", rel: "about.tsx", want: "/about"},
		{name: "nested index", rel: "blog/index.tsx", want: "/blog"},
		{name: "dynamic segment", rel: "blog/[id].tsx", want: "/blog/{id}"},
		{name: "catch-all segment", rel: "docs/[...slug].tsx", want: "/docs/{slug...}"},
		{name: "404 page", rel: "404.tsx", want: "/404"},
		{name: "deep nesting", rel: "a/b/c.tsx", want: "/a/b/c"},
		{name: "multi dynamic", rel: "[cat]/[id].tsx", want: "/{cat}/{id}"},
		{name: "deep nested index", rel: "deep/nested/index.tsx", want: "/deep/nested"},
		{name: ".ts extension", rel: "page.ts", want: "/page"},
		{name: ".jsx extension", rel: "page.jsx", want: "/page"},
		{name: ".js extension", rel: "page.js", want: "/page"},
		{name: "invalid param name", rel: "docs/[123].tsx", errContains: `invalid parameter name "123"`},
		{name: "missing catch-all name", rel: "docs/[...].tsx", errContains: "missing parameter name"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := fileToPattern(tc.rel)
			if tc.errContains != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errContains)
				}
				if !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error = %q, want to contain %q", err.Error(), tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("fileToPattern(%q) = %q, want %q", tc.rel, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------
func TestScanHappyPath(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pagesDir, "index.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "about.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "[id].tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "docs", "[...slug].tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "404.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "README.md"), "")

	routes, err := Scan(pagesDir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := map[string]string{
		"/":               "index",
		"/about":          "about",
		"/blog/{id}":      "blog/[id]",
		"/docs/{slug...}": "docs/[...slug]",
		"/404":            "404",
	}
	if len(routes) != len(want) {
		t.Errorf("got %d routes, want %d", len(routes), len(want))
	}
	for _, r := range routes {
		wantKey, ok := want[r.Pattern]
		if !ok {
			t.Errorf("unexpected pattern %q", r.Pattern)
			continue
		}
		if r.BundleKey != wantKey {
			t.Errorf("pattern %q: BundleKey = %q, want %q", r.Pattern, r.BundleKey, wantKey)
		}
		if r.FilePath == "" {
			t.Errorf("pattern %q: FilePath is empty", r.Pattern)
		}
	}
}

func TestScanDetectsConflictingRoutes(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pagesDir, "blog.tsx"), "export default function Blog() { return null; }")
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "index.tsx"), "export default function BlogIndex() { return null; }")

	_, err := Scan(pagesDir)
	if err == nil {
		t.Fatal("expected route conflict error, got nil")
	}
	if !strings.Contains(err.Error(), `conflicting routes for pattern "/blog"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanRejectsReservedPrefix(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pagesDir, "_echo", "internal.tsx"), "export default function Internal() { return null; }")

	_, err := Scan(pagesDir)
	if err == nil {
		t.Fatal("expected reserved prefix error, got nil")
	}
	if !strings.Contains(err.Error(), "reserved path prefix /_echo") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanInvalidParamName(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pagesDir, "[123invalid].tsx"), "")

	_, err := Scan(pagesDir)
	if err == nil {
		t.Fatal("expected error for invalid param name, got nil")
	}
}

func TestScanSortedDeterministically(t *testing.T) {
	t.Parallel()

	pagesDir := t.TempDir()
	mustWriteFile(t, filepath.Join(pagesDir, "z.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "a.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "m.tsx"), "")

	routes, err := Scan(pagesDir)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for i := 1; i < len(routes); i++ {
		if routes[i].Pattern < routes[i-1].Pattern {
			t.Errorf("routes not sorted: %q before %q", routes[i-1].Pattern, routes[i].Pattern)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
func mustWriteFile(t *testing.T, path string, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
