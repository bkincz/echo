package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEchoPagesFileToPattern(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"index.tsx", "/"},
		{"about.tsx", "/about"},
		{"blog/index.tsx", "/blog"},
		{"blog/[id].tsx", "/blog/{id}"},
		{"files/[...path].tsx", "/files/{path...}"},
		{"a/b/c.tsx", "/a/b/c"},
	}
	for _, c := range cases {
		got := echoPagesFileToPattern(c.in)
		if got != c.want {
			t.Errorf("echoPagesFileToPattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEchoPagesFindLayouts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pagesDir := filepath.Join(dir, "pages")

	mustMkdir(t, pagesDir)
	mustMkdir(t, filepath.Join(pagesDir, "blog"))
	mustWriteFile(t, filepath.Join(pagesDir, "_layout.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "_layout.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "[id].tsx"), "")

	t.Run("page at root has root layout", func(t *testing.T) {
		t.Parallel()
		layouts := echoPagesFindLayouts(pagesDir, "index.tsx")
		if len(layouts) != 1 || layouts[0] != "_layout.tsx" {
			t.Fatalf("layouts = %v, want [_layout.tsx]", layouts)
		}
	})

	t.Run("nested page has root + dir layout chain", func(t *testing.T) {
		t.Parallel()
		layouts := echoPagesFindLayouts(pagesDir, "blog/[id].tsx")
		if len(layouts) != 2 {
			t.Fatalf("layouts = %v, want 2 entries", layouts)
		}
		if layouts[0] != "_layout.tsx" {
			t.Errorf("layouts[0] = %q, want _layout.tsx", layouts[0])
		}
		if layouts[1] != "blog/_layout.tsx" {
			t.Errorf("layouts[1] = %q, want blog/_layout.tsx", layouts[1])
		}
	})

	t.Run("page with no layouts returns empty", func(t *testing.T) {
		t.Parallel()
		emptyDir := t.TempDir()
		mustMkdir(t, filepath.Join(emptyDir, "pages"))
		layouts := echoPagesFindLayouts(filepath.Join(emptyDir, "pages"), "index.tsx")
		if len(layouts) != 0 {
			t.Fatalf("layouts = %v, want empty", layouts)
		}
	})
}

func TestIsEchoPageSkipped(t *testing.T) {
	t.Parallel()
	skipped := []string{"_layout.tsx", "404.tsx", "500.tsx", "page.loader.ts", "page.meta.json", "types.d.ts"}
	for _, name := range skipped {
		if !isEchoPageSkipped(name) {
			t.Errorf("isEchoPageSkipped(%q) = false, want true", name)
		}
	}
	kept := []string{"index.tsx", "about.tsx", "blog.tsx", "[id].tsx"}
	for _, name := range kept {
		if isEchoPageSkipped(name) {
			t.Errorf("isEchoPageSkipped(%q) = true, want false", name)
		}
	}
}

func TestGenerateEchoPagesModule(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	pagesDir := filepath.Join(appDir, "pages")
	mustMkdir(t, pagesDir)
	mustWriteFile(t, filepath.Join(pagesDir, "_layout.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "index.tsx"), "")
	mustMkdir(t, filepath.Join(pagesDir, "blog"))
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "[id].tsx"), "")

	got, err := generateEchoPagesModule(appDir)
	if err != nil {
		t.Fatalf("generateEchoPagesModule: %v", err)
	}

	if !strings.Contains(got, `"/"`) {
		t.Errorf("expected root pattern in output:\n%s", got)
	}
	if !strings.Contains(got, `"/blog/{id}"`) {
		t.Errorf("expected /blog/{id} pattern in output:\n%s", got)
	}
	if !strings.Contains(got, "layouts:") {
		t.Errorf("expected layouts field in output:\n%s", got)
	}
	// Root layout should appear for both pages.
	if count := strings.Count(got, "_layout.tsx"); count < 2 {
		t.Errorf("expected _layout.tsx at least twice (once per page), got %d:\n%s", count, got)
	}
	// Pattern conversion utility must be exported.
	if !strings.Contains(got, "export function echoPatternToPath") {
		t.Errorf("expected echoPatternToPath export in output:\n%s", got)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
