package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// patternToFilePath
// ---------------------------------------------------------------------------
func TestPatternToFilePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		params  map[string]string
		want    string
	}{
		{"/", nil, "index.html"},
		{"/about", nil, "about/index.html"},
		{"/blog/{id}", map[string]string{"id": "42"}, "blog/42/index.html"},
		{"/docs/{slug...}", map[string]string{"slug": "getting-started"}, "docs/getting-started/index.html"},
		{"/a/b/c", nil, "a/b/c/index.html"},
	}

	for _, tc := range cases {
		got := patternToFilePath(tc.pattern, tc.params)
		if got != tc.want {
			t.Errorf("patternToFilePath(%q, %v) = %q, want %q", tc.pattern, tc.params, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// isDynamicPattern
// ---------------------------------------------------------------------------
func TestIsDynamicPattern(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		want    bool
	}{
		{"/", false},
		{"/about", false},
		{"/blog/{id}", true},
		{"/docs/{slug...}", true},
	}

	for _, tc := range cases {
		got := isDynamicPattern(tc.pattern)
		if got != tc.want {
			t.Errorf("isDynamicPattern(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// syntheticRequest
// ---------------------------------------------------------------------------
func TestSyntheticRequest(t *testing.T) {
	t.Parallel()

	req := syntheticRequest("/blog/{id}", map[string]string{"id": "123"})
	if req == nil {
		t.Fatal("syntheticRequest returned nil")
	}
	if got := req.PathValue("id"); got != "123" {
		t.Errorf("PathValue(id) = %q, want %q", got, "123")
	}
	if req.Method != "GET" {
		t.Errorf("Method = %q, want GET", req.Method)
	}
}

// ---------------------------------------------------------------------------
// resolvePathEntries
// ---------------------------------------------------------------------------
func TestResolvePathEntries(t *testing.T) {
	t.Parallel()

	logger := slog.Default()

	t.Run("static route returns single empty-params entry", func(t *testing.T) {
		t.Parallel()
		mr := manifestRoute{Pattern: "/about", BundleKey: "about"}
		entries, err := resolvePathEntries(mr, nil, nil, nil, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if len(entries[0]) != 0 {
			t.Errorf("expected empty params map, got %v", entries[0])
		}
	})

	t.Run("dynamic route with Go PathsFunc returns its entries", func(t *testing.T) {
		t.Parallel()
		mr := manifestRoute{Pattern: "/blog/{id}", BundleKey: "blog/[id]"}
		goPaths := map[string]PathsFunc{
			"/blog/{id}": func() ([]map[string]string, error) {
				return []map[string]string{{"id": "1"}, {"id": "2"}}, nil
			},
		}
		entries, err := resolvePathEntries(mr, nil, nil, goPaths, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if entries[0]["id"] != "1" || entries[1]["id"] != "2" {
			t.Errorf("unexpected entries: %v", entries)
		}
	})

	t.Run("Go PathsFunc error is propagated", func(t *testing.T) {
		t.Parallel()
		mr := manifestRoute{Pattern: "/blog/{id}", BundleKey: "blog/[id]"}
		goPaths := map[string]PathsFunc{
			"/blog/{id}": func() ([]map[string]string, error) {
				return nil, fmt.Errorf("paths failed")
			},
		}
		_, err := resolvePathEntries(mr, nil, nil, goPaths, logger)
		if err == nil {
			t.Fatal("expected error from failing PathsFunc")
		}
	})

	t.Run("dynamic route with no paths provider returns nil", func(t *testing.T) {
		t.Parallel()
		mr := manifestRoute{Pattern: "/blog/{id}", BundleKey: "blog/[id]"}
		entries, err := resolvePathEntries(mr, nil, nil, nil, logger)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entries != nil {
			t.Errorf("expected nil entries for dynamic route without paths, got %v", entries)
		}
	})
}

func TestBuildStaticRendersDynamicRoutesWithLoaderData(t *testing.T) {
	appDir := createSmokeApp(t)

	fake := &fakeFrontendEngine{}
	err := BuildStatic(appDir, BuildOptions{
		Logger:           discardLogger(),
		Frontend:         fake,
		FrontendSSREntry: "src/entry-server.tsx",
		GoPaths: map[string]PathsFunc{
			"/blog/{id}": func() ([]map[string]string, error) {
				return []map[string]string{{"id": "one"}, {"id": "two"}}, nil
			},
		},
		GoLoaders: map[string]LoaderFunc{
			"/": func(_ *http.Request) (any, error) {
				return map[string]string{"page": "home"}, nil
			},
			"/blog/{id}": func(r *http.Request) (any, error) {
				return map[string]string{"id": r.PathValue("id")}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildStatic: %v", err)
	}

	if fake.buildClientCalls != 1 {
		t.Fatalf("client build calls = %d, want 1", fake.buildClientCalls)
	}
	if fake.buildServerCalls != 1 {
		t.Fatalf("server build calls = %d, want 1", fake.buildServerCalls)
	}
	if fake.renderCalls != 5 {
		t.Fatalf("render calls = %d, want 5", fake.renderCalls)
	}

	byURL := make(map[string]json.RawMessage, len(fake.renderHistory))
	for _, call := range fake.renderHistory {
		byURL[call.URL] = call.LoaderData
	}

	assertLoader := func(url string, want map[string]string) {
		t.Helper()
		raw, ok := byURL[url]
		if !ok {
			t.Fatalf("missing render call for %s", url)
		}
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal loader data for %s: %v", url, err)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("loader data for %s[%s] = %q, want %q (full: %v)", url, k, got[k], v, got)
			}
		}
	}

	assertLoader("/", map[string]string{"page": "home"})
	assertLoader("/blog/one", map[string]string{"id": "one"})
	assertLoader("/blog/two", map[string]string{"id": "two"})

	for _, rel := range []string{
		"dist/index.html",
		"dist/blog/one/index.html",
		"dist/blog/two/index.html",
	} {
		full := filepath.Join(appDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(data), "rendered") {
			t.Fatalf("%s missing rendered marker: %q", rel, string(data))
		}
	}
}

func TestBuildStaticFailsWhenGoLoaderReturnsError(t *testing.T) {
	appDir := createSmokeApp(t)

	wantErr := errors.New("loader broke")
	fake := &fakeFrontendEngine{}
	err := BuildStatic(appDir, BuildOptions{
		Logger:           discardLogger(),
		Frontend:         fake,
		FrontendSSREntry: "src/entry-server.tsx",
		GoLoaders: map[string]LoaderFunc{
			"/": func(_ *http.Request) (any, error) {
				return nil, wantErr
			},
		},
	})
	if err == nil {
		t.Fatal("expected loader error, got nil")
	}
	if !strings.Contains(err.Error(), "loader for /: loader broke") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildStaticRejectsPathTraversalFromDynamicParams(t *testing.T) {
	appDir := createSmokeApp(t)

	fake := &fakeFrontendEngine{}
	err := BuildStatic(appDir, BuildOptions{
		Logger:           discardLogger(),
		Frontend:         fake,
		FrontendSSREntry: "src/entry-server.tsx",
		GoPaths: map[string]PathsFunc{
			"/blog/{id}": func() ([]map[string]string, error) {
				return []map[string]string{{"id": "../../escape"}}, nil
			},
		},
	})
	if err == nil {
		t.Fatal("expected traversal path error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildStaticUsesConfiguredPagesDir(t *testing.T) {
	appDir := createSmokeApp(t)

	oldPagesDir := filepath.Join(appDir, "pages")
	newPagesDir := filepath.Join(appDir, "src", "pages")
	if err := os.MkdirAll(filepath.Dir(newPagesDir), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.Rename(oldPagesDir, newPagesDir); err != nil {
		t.Fatalf("move pages dir: %v", err)
	}

	cfg := []byte(`{"paths":{"pagesDir":"src/pages"}}`)
	if err := os.WriteFile(filepath.Join(appDir, "echo.config.json"), cfg, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	fake := &fakeFrontendEngine{}
	err := BuildStatic(appDir, BuildOptions{
		Logger:           discardLogger(),
		Frontend:         fake,
		FrontendSSREntry: "src/entry-server.tsx",
		GoPaths: map[string]PathsFunc{
			"/blog/{id}": func() ([]map[string]string, error) {
				return []map[string]string{{"id": "configured"}}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildStatic: %v", err)
	}

	if fake.renderCalls == 0 {
		t.Fatal("expected at least one render call")
	}

	for _, rel := range []string{
		"dist/index.html",
		"dist/blog/configured/index.html",
	} {
		full := filepath.Join(appDir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Fatalf("expected %s to exist: %v", rel, err)
		}
	}
}
