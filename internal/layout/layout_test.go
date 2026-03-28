package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindNoLayouts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "index.tsx"))
	touch(t, filepath.Join(dir, "about.tsx"))

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestFindRootLayout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "_layout.tsx"))
	touch(t, filepath.Join(dir, "index.tsx"))
	touch(t, filepath.Join(dir, "about.tsx"))

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	for _, key := range []string{"index", "about"} {
		chain := got[key]
		if len(chain) != 1 {
			t.Errorf("key %q: want 1 layout, got %d: %v", key, len(chain), chain)
		}
	}
}

func TestFindNestedLayouts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "_layout.tsx"))
	touch(t, filepath.Join(dir, "index.tsx"))
	touch(t, filepath.Join(dir, "blog", "_layout.tsx"))
	touch(t, filepath.Join(dir, "blog", "[id].tsx"))

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if len(got["index"]) != 1 {
		t.Errorf("index: want 1 layout, got %v", got["index"])
	}

	chain := got["blog/[id]"]
	if len(chain) != 2 {
		t.Fatalf("blog/[id]: want 2 layouts, got %v", chain)
	}
}

func TestFindSkipsNonPageCompanions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	touch(t, filepath.Join(dir, "_layout.tsx"))
	touch(t, filepath.Join(dir, "index.tsx"))
	touch(t, filepath.Join(dir, "index.loader.ts"))
	touch(t, filepath.Join(dir, "_private.tsx"))
	touch(t, filepath.Join(dir, "types.d.ts"))

	got, err := Find(dir)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}

	if _, ok := got["index.loader"]; ok {
		t.Fatalf("loader sidecar should not receive a layout chain: %v", got)
	}
	if _, ok := got["_private"]; ok {
		t.Fatalf("underscore-prefixed files should not receive a layout chain: %v", got)
	}
	if len(got["index"]) != 1 {
		t.Fatalf("index: want root layout chain, got %v", got["index"])
	}
}

func TestIsLayoutFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"pages/_layout.tsx", true},
		{"pages/_layout.ts", true},
		{"pages/_layout.jsx", true},
		{"pages/_layout.js", true},
		{"pages/layout.tsx", false},
		{"pages/index.tsx", false},
		{"pages/blog/_layout.tsx", true},
	}
	for _, tc := range cases {
		if got := IsLayoutFile(tc.path); got != tc.want {
			t.Errorf("IsLayoutFile(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
