package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateEchoPagesModule(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	pagesDir := filepath.Join(appDir, "pages")
	mustMkdir(t, pagesDir)
	mustWriteFile(t, filepath.Join(pagesDir, "_layout.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "index.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "404.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "index.loader.ts"), "")
	mustMkdir(t, filepath.Join(pagesDir, "blog"))
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "_layout.tsx"), "")
	mustWriteFile(t, filepath.Join(pagesDir, "blog", "[id].tsx"), "")

	got, err := generateEchoPagesModule(appDir, pagesDir, "/admin")
	if err != nil {
		t.Fatalf("generateEchoPagesModule: %v", err)
	}

	if !strings.Contains(got, `"/"`) {
		t.Errorf("expected root pattern in output:\n%s", got)
	}
	if !strings.Contains(got, `"/blog/{id}"`) {
		t.Errorf("expected /blog/{id} pattern in output:\n%s", got)
	}
	if strings.Contains(got, `"/404"`) {
		t.Errorf("did not expect special error page route in output:\n%s", got)
	}
	if strings.Contains(got, `"/index.loader"`) {
		t.Errorf("did not expect loader sidecar route in output:\n%s", got)
	}
	if !strings.Contains(got, "layouts:") {
		t.Errorf("expected layouts field in output:\n%s", got)
	}
	// Root layout should appear for both pages and the blog layout for blog pages.
	if count := strings.Count(got, "_layout.tsx"); count < 3 {
		t.Errorf("expected layout imports to be present for root and nested pages, got %d:\n%s", count, got)
	}
	// Pattern conversion utility must be exported.
	if !strings.Contains(got, "export function echoPatternToPath") {
		t.Errorf("expected echoPatternToPath export in output:\n%s", got)
	}
	if !strings.Contains(got, `export const echoBasePath = "/admin"`) {
		t.Errorf("expected echoBasePath export in output:\n%s", got)
	}
	if !strings.Contains(got, "export function echoDataPath") {
		t.Errorf("expected echoDataPath export in output:\n%s", got)
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
