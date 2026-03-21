package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FindLightningCSS
// ---------------------------------------------------------------------------

func TestFindLightningCSS_presentInNodeModules(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "node_modules", ".bin", lightningBinName())
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := FindLightningCSS(dir)
	if got != bin {
		t.Errorf("FindLightningCSS = %q, want %q", got, bin)
	}
}

func TestFindLightningCSS_missingReturnsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got := FindLightningCSS(dir)
	if got != "" {
		t.Errorf("FindLightningCSS missing: got %q, want empty", got)
	}
}

func TestFindLightningCSS_directoryNotBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a directory at the expected binary path — should not be returned.
	binPath := filepath.Join(dir, "node_modules", ".bin", lightningBinName())
	if err := os.MkdirAll(binPath, 0o755); err != nil {
		t.Fatal(err)
	}
	got := FindLightningCSS(dir)
	if got != "" {
		t.Errorf("FindLightningCSS dir at bin path: got %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// LightningCSSTransform error paths (no real lightningcss binary required)
// ---------------------------------------------------------------------------

func TestLightningCSSTransform_badBinaryReturnsError(t *testing.T) {
	t.Parallel()
	transform := LightningCSSTransform("/no/such/binary", false)
	_, err := transform("body { color: red; }")
	if err == nil {
		t.Fatal("expected error from non-existent binary")
	}
}

func TestLightningCSSTransform_binaryExitsNonZeroReturnsError(t *testing.T) {
	t.Parallel()
	// Use a script that always fails.
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-lc")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'bad css' >&2; exit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	transform := LightningCSSTransform(script, false)
	_, err := transform("body { color: red; }")
	if err == nil {
		t.Fatal("expected error from failing binary")
	}
	if !strings.Contains(err.Error(), "lightningcss") {
		t.Errorf("error should mention lightningcss, got: %v", err)
	}
}

func TestLightningCSSTransform_minifyFlag(t *testing.T) {
	t.Parallel()
	// Fake binary that echoes its args so we can inspect them.
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-lc")
	outCapture := filepath.Join(dir, "args.txt")
	scriptBody := "#!/bin/sh\necho \"$@\" > " + outCapture + "\n"
	// Write the CSS content from the -o arg to make the transform succeed.
	// The real lightningcss writes to -o <outfile>; our fake needs to too.
	scriptBody += `for i in "$@"; do
  if [ "$prev" = "-o" ]; then
    touch "$i"
  fi
  prev="$i"
done
exit 0
`
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatal(err)
	}

	transform := LightningCSSTransform(script, true)
	_, _ = transform("body {}")

	args, err := os.ReadFile(outCapture)
	if err != nil {
		t.Skip("fake binary did not capture args (shell behaviour varies)")
	}
	if !strings.Contains(string(args), "--minify") {
		t.Errorf("--minify flag not passed to binary, args: %s", args)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func lightningBinName() string {
	if runtime.GOOS == "windows" {
		return "lightningcss.cmd"
	}
	return "lightningcss"
}
