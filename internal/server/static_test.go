package server

import (
	"fmt"
	"log/slog"
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
