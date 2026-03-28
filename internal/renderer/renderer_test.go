package renderer

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Shell — defaults and structure
// ---------------------------------------------------------------------------
func TestShellDefaults(t *testing.T) {
	t.Parallel()
	html, err := Shell(ShellOptions{BundleURL: "/_echo/bundle/abc.js"})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if !strings.Contains(html, "<title>Echo</title>") {
		t.Error("expected default title 'Echo'")
	}
}

func TestShellTitle(t *testing.T) {
	t.Parallel()
	html, err := Shell(ShellOptions{Title: "About", BundleURL: "/_echo/bundle/abc.js"})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if !strings.Contains(html, "<title>About</title>") {
		t.Errorf("expected title 'About' in output:\n%s", html)
	}
}

func TestShellBundleScript(t *testing.T) {
	t.Parallel()
	html, err := Shell(ShellOptions{BundleURL: "/_echo/bundle/deadbeef.js"})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	want := `src="/_echo/bundle/deadbeef.js"`
	if !strings.Contains(html, want) {
		t.Errorf("expected %q in output", want)
	}
}

func TestShellStructure(t *testing.T) {
	t.Parallel()
	html, err := Shell(ShellOptions{
		Title:     "Test",
		BundleURL: "/_echo/bundle/abc.js",
	})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	for _, want := range []string{
		"<!DOCTYPE html>",
		`<html lang="en">`,
		"<head>",
		"</head>",
		"<body>",
		`<div id="root">`,
		"</body>",
		"</html>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q in output", want)
		}
	}
}

// ---------------------------------------------------------------------------
// Shell — optional metadata
// ---------------------------------------------------------------------------
func TestShellDescriptionMeta(t *testing.T) {
	t.Parallel()

	t.Run("present when set", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			BundleURL:   "/_echo/bundle/abc.js",
			Description: "A test page",
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		want := `<meta name="description" content="A test page" />`
		if !strings.Contains(html, want) {
			t.Errorf("expected description meta tag in output:\n%s", html)
		}
	})

	t.Run("absent when empty", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{BundleURL: "/_echo/bundle/abc.js"})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if strings.Contains(html, `name="description"`) {
			t.Error("description meta should be absent when not set")
		}
	})
}

func TestShellCSSLink(t *testing.T) {
	t.Parallel()

	t.Run("present when set", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			BundleURL:    "/_echo/bundle/abc.js",
			CSSBundleURL: "/_echo/bundle/abc.css",
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		want := `<link rel="stylesheet" href="/_echo/bundle/abc.css" />`
		if !strings.Contains(html, want) {
			t.Errorf("expected CSS link tag in output:\n%s", html)
		}
	})

	t.Run("absent when empty", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{BundleURL: "/_echo/bundle/abc.js"})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if strings.Contains(html, `rel="stylesheet"`) {
			t.Error("stylesheet link should be absent when CSSBundleURL is empty")
		}
	})
}

// ---------------------------------------------------------------------------
// Shell — dev mode
// ---------------------------------------------------------------------------
func TestShellDevMode(t *testing.T) {
	t.Parallel()

	t.Run("SSE script present in dev mode", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			BundleURL: "/_echo/bundle/abc.js",
			SSEURL:    "/admin/_echo/sse",
			DevMode:   true,
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if !strings.Contains(html, "/admin/_echo/sse") {
			t.Error("expected SSE endpoint reference in dev mode output")
		}
		if !strings.Contains(html, "EventSource") {
			t.Error("expected EventSource in dev mode output")
		}
	})

	t.Run("SSE script absent in production mode", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			BundleURL: "/_echo/bundle/abc.js",
			DevMode:   false,
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if strings.Contains(html, "/_echo/sse") {
			t.Error("SSE script should not appear in production mode")
		}
	})
}

// ---------------------------------------------------------------------------
// Shell — XSS escaping
// ---------------------------------------------------------------------------
func TestShellXSSEscaping(t *testing.T) {
	t.Parallel()

	t.Run("title is escaped", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			Title:     "<script>alert('xss')</script>",
			BundleURL: "/_echo/bundle/abc.js",
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if strings.Contains(html, "<script>alert") {
			t.Error("title must be HTML-escaped")
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Error("expected escaped title in output")
		}
	})

	t.Run("description quote is escaped", func(t *testing.T) {
		t.Parallel()
		html, err := Shell(ShellOptions{
			Description: `evil"attr`,
			BundleURL:   "/_echo/bundle/abc.js",
		})
		if err != nil {
			t.Fatalf("Shell: %v", err)
		}
		if strings.Contains(html, `content="evil"attr`) {
			t.Error("unescaped quote in description would allow attribute injection")
		}
	})
}
