package bundler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// ---------------------------------------------------------------------------
// joinMessages
// ---------------------------------------------------------------------------

func TestJoinMessages(t *testing.T) {
	t.Parallel()

	t.Run("empty returns fallback", func(t *testing.T) {
		t.Parallel()
		got := joinMessages(nil)
		if got != "unknown esbuild error" {
			t.Errorf("joinMessages(nil) = %q, want fallback", got)
		}
	})

	t.Run("single message", func(t *testing.T) {
		t.Parallel()
		msgs := []api.Message{{Text: "syntax error"}}
		got := joinMessages(msgs)
		if got != "syntax error" {
			t.Errorf("joinMessages = %q, want %q", got, "syntax error")
		}
	})

	t.Run("multiple messages joined with semicolon", func(t *testing.T) {
		t.Parallel()
		msgs := []api.Message{{Text: "err A"}, {Text: "err B"}}
		got := joinMessages(msgs)
		if got != "err A; err B" {
			t.Errorf("joinMessages = %q, want %q", got, "err A; err B")
		}
	})
}

// ---------------------------------------------------------------------------
// parseInputs
// ---------------------------------------------------------------------------

func TestParseInputs(t *testing.T) {
	t.Parallel()

	t.Run("empty metafile returns nil", func(t *testing.T) {
		t.Parallel()
		got, err := parseInputs("", "/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("stdin excluded", func(t *testing.T) {
		t.Parallel()
		meta := map[string]interface{}{
			"inputs": map[string]interface{}{
				"<stdin>": map[string]interface{}{},
			},
		}
		data, _ := json.Marshal(meta)
		got, err := parseInputs(string(data), "/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected stdin to be excluded, got %v", got)
		}
	})

	t.Run("relative paths resolved against appDir", func(t *testing.T) {
		t.Parallel()
		appDir := t.TempDir()
		meta := map[string]interface{}{
			"inputs": map[string]interface{}{
				"pages/index.tsx":     map[string]interface{}{},
				"components/hero.tsx": map[string]interface{}{},
			},
		}
		data, _ := json.Marshal(meta)
		got, err := parseInputs(string(data), appDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 inputs, got %d", len(got))
		}
		appSlash := filepath.ToSlash(appDir)
		for _, p := range got {
			if !strings.HasPrefix(p, appSlash) {
				t.Errorf("expected path rooted under appDir %q, got %q", appSlash, p)
			}
		}
	})

	t.Run("absolute paths kept as-is", func(t *testing.T) {
		t.Parallel()
		abs := filepath.ToSlash(filepath.Join("/external", "lib.tsx"))
		meta := map[string]interface{}{
			"inputs": map[string]interface{}{
				abs: map[string]interface{}{},
			},
		}
		data, _ := json.Marshal(meta)
		got, err := parseInputs(string(data), "/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 input, got %d", len(got))
		}
	})

	t.Run("duplicate paths deduplicated", func(t *testing.T) {
		t.Parallel()
		meta := map[string]interface{}{
			"inputs": map[string]interface{}{
				"pages/index.tsx":   map[string]interface{}{},
				"./pages/index.tsx": map[string]interface{}{},
			},
		}
		data, _ := json.Marshal(meta)
		got, err := parseInputs(string(data), "/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) > 2 {
			t.Errorf("expected at most 2 entries after dedup, got %d: %v", len(got), got)
		}
	})

	t.Run("result is sorted", func(t *testing.T) {
		t.Parallel()
		meta := map[string]interface{}{
			"inputs": map[string]interface{}{
				"z.tsx": map[string]interface{}{},
				"a.tsx": map[string]interface{}{},
				"m.tsx": map[string]interface{}{},
			},
		}
		data, _ := json.Marshal(meta)
		got, err := parseInputs(string(data), "/app")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1] {
				t.Errorf("result not sorted: %q before %q", got[i-1], got[i])
			}
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		t.Parallel()
		_, err := parseInputs("not json", "/app")
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})
}

// ---------------------------------------------------------------------------
// Compiler.Build
// ---------------------------------------------------------------------------

func TestCompilerBuildCSS(t *testing.T) {
	t.Run("withCSS", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "client.ts"), []byte(`export function mount() {}`), 0o644); err != nil {
			t.Fatal(err)
		}
		cssPath := filepath.Join(dir, "styles.css")
		if err := os.WriteFile(cssPath, []byte(`body { margin: 0; }`), 0o644); err != nil {
			t.Fatal(err)
		}
		pagePath := filepath.Join(dir, "page.ts")
		if err := os.WriteFile(pagePath, []byte(`import "./styles.css"; export default function Page() { return null; }`), 0o644); err != nil {
			t.Fatal(err)
		}

		c, err := NewCompiler(Options{AppDir: dir})
		if err != nil {
			t.Fatalf("NewCompiler: %v", err)
		}
		defer c.Close()

		b, err := c.Build(pagePath, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if b.JS == "" {
			t.Error("expected non-empty JS output")
		}
		if b.CSS == "" {
			t.Error("expected non-empty CSS output — CSS import should produce a separate bundle")
		}
		wantInput := filepath.ToSlash(cssPath)
		found := false
		for _, inp := range b.Inputs {
			if inp == wantInput {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected CSS file %q in Bundle.Inputs, got: %v", wantInput, b.Inputs)
		}
	})

	t.Run("noCSS", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "client.ts"), []byte(`export function mount() {}`), 0o644); err != nil {
			t.Fatal(err)
		}
		pagePath := filepath.Join(dir, "page.ts")
		if err := os.WriteFile(pagePath, []byte(`export default function Page() { return null; }`), 0o644); err != nil {
			t.Fatal(err)
		}

		c, err := NewCompiler(Options{AppDir: dir})
		if err != nil {
			t.Fatalf("NewCompiler: %v", err)
		}
		defer c.Close()

		b, err := c.Build(pagePath, nil)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if b.JS == "" {
			t.Error("expected non-empty JS output")
		}
		if b.CSS != "" {
			t.Errorf("expected empty CSS output, got %d bytes", len(b.CSS))
		}
	})
}

// ---------------------------------------------------------------------------
// findClientEntry
// ---------------------------------------------------------------------------

func TestFindClientEntry(t *testing.T) {
	t.Parallel()

	candidates := []string{"client.tsx", "client.ts", "client.jsx", "client.js"}

	for _, name := range candidates {
		name := name
		t.Run("finds "+name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, name), []byte("export function mount(){}"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := findClientEntry(dir)
			if err != nil {
				t.Fatalf("findClientEntry: %v", err)
			}
			if !strings.HasSuffix(got, name) {
				t.Errorf("got %q, want suffix %q", got, name)
			}
		})
	}

	t.Run("missing client returns error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		_, err := findClientEntry(dir)
		if err == nil {
			t.Fatal("expected error for missing client adapter")
		}
		if !strings.Contains(err.Error(), "missing client adapter") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("directory named client.tsx is skipped", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "client.tsx"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := findClientEntry(dir)
		if err == nil {
			t.Error("expected error — client.tsx is a directory, not a file")
		}
	})

	t.Run("prefers tsx over ts", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		for _, name := range []string{"client.tsx", "client.ts"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := findClientEntry(dir)
		if err != nil {
			t.Fatalf("findClientEntry: %v", err)
		}
		if !strings.HasSuffix(got, "client.tsx") {
			t.Errorf("expected client.tsx to win, got %q", got)
		}
	})
}
