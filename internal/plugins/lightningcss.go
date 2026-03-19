package plugins

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ---------------------------------------------------------------------------
// Lightning CSS
// ---------------------------------------------------------------------------
func FindLightningCSS(appDir string) string {
	name := "lightningcss"
	if runtime.GOOS == "windows" {
		name = "lightningcss.cmd"
	}
	abs := filepath.Join(appDir, "node_modules", ".bin", name)
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return abs
	}
	return ""
}

func LightningCSSTransform(binary string, minify bool) func(string) (string, error) {
	return func(css string) (string, error) {
		in, err := os.CreateTemp("", "echo-lc-in-*.css")
		if err != nil {
			return "", fmt.Errorf("lightningcss: create temp input: %w", err)
		}
		defer os.Remove(in.Name())
		if _, err := in.WriteString(css); err != nil {
			in.Close()
			return "", fmt.Errorf("lightningcss: write temp input: %w", err)
		}
		in.Close()

		out, err := os.CreateTemp("", "echo-lc-out-*.css")
		if err != nil {
			return "", fmt.Errorf("lightningcss: create temp output: %w", err)
		}
		outName := out.Name()
		out.Close()
		defer os.Remove(outName)

		args := []string{
			"--browserslist",
			"--nesting",
			in.Name(), "-o", outName,
		}
		if minify {
			args = append([]string{"--minify"}, args...)
		}

		cmd := exec.Command(binary, args...)
		if combined, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("lightningcss: %w\n%s", err, combined)
		}

		result, err := os.ReadFile(outName)
		if err != nil {
			return "", fmt.Errorf("lightningcss: read output: %w", err)
		}
		return string(result), nil
	}
}
