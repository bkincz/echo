package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const helperEnvKey = "GO_ECHO_HELPER_PROCESS"

func TestEchoCLIRequiresNodeForDevBuildStart(t *testing.T) {
	t.Parallel()

	for _, cmdName := range []string{"dev", "build", "start"} {
		cmdName := cmdName
		t.Run(cmdName, func(t *testing.T) {
			t.Parallel()

			appDir := t.TempDir()
			out, exit := runEchoHelperProcess(t, appDir, []string{
				helperEnvKey + "=1",
				"PATH=", // Simulate a shell environment with no node runtime available.
			}, "echo", cmdName, appDir)

			if exit == 0 {
				t.Fatalf("expected non-zero exit code for %s, got 0; output:\n%s", cmdName, out)
			}
			if !strings.Contains(out, "Node.js is required but was not found in PATH") {
				t.Fatalf("expected Node requirement error for %s, got:\n%s", cmdName, out)
			}
		})
	}
}

func TestEchoCLIBuildMissingViteShowsActionableError(t *testing.T) {
	t.Parallel()

	appDir := t.TempDir()
	fakeBinDir := t.TempDir()
	if err := writeFakeNodeBinary(fakeBinDir); err != nil {
		t.Fatalf("write fake node: %v", err)
	}

	out, exit := runEchoHelperProcess(t, appDir, []string{
		helperEnvKey + "=1",
		"PATH=" + fakeBinDir,
	}, "echo", "build", appDir)

	if exit == 0 {
		t.Fatalf("expected non-zero exit code, got 0; output:\n%s", out)
	}
	if !strings.Contains(out, "vite not found. install it in app dependencies") {
		t.Fatalf("expected actionable vite error, got:\n%s", out)
	}
}

func TestEchoCLIStartWithoutBuildShowsManifestHint(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node is required for this test: %v", err)
	}

	appDir := t.TempDir()
	out, exit := runEchoHelperProcess(t, appDir, []string{
		helperEnvKey + "=1",
	}, "echo", "start", appDir)

	if exit == 0 {
		t.Fatalf("expected non-zero exit code, got 0; output:\n%s", out)
	}
	if !strings.Contains(out, "reading dist/manifest.json") {
		t.Fatalf("expected manifest read failure hint, got:\n%s", out)
	}
	if !strings.Contains(out, "run 'echo build") {
		t.Fatalf("expected build hint in output, got:\n%s", out)
	}
}

func TestEchoCLIInitScaffoldsReactViteDefaults(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "my-app")
	out, exit := runEchoHelperProcess(t, root, []string{
		helperEnvKey + "=1",
	}, "echo", "init", appDir)
	if exit != 0 {
		t.Fatalf("expected init to succeed, got exit %d:\n%s", exit, out)
	}

	mustContainFile(t, filepath.Join(appDir, "src", "entry-server.tsx"), "export async function render")
	mustContainFile(t, filepath.Join(appDir, "vite.config.ts"), "@vitejs/plugin-react")
	mustContainFile(t, filepath.Join(appDir, "plugins", "echo-pages.ts"), "echoConfig.paths?.pagesDir || \"pages\"")
	mustContainFile(t, filepath.Join(appDir, "package.json"), "\"vite\"")
	mustContainFile(t, filepath.Join(appDir, "index.html"), "src=\"/src/main.ts\"")
	if !strings.Contains(out, "npm install") {
		t.Fatalf("expected default npm install hint, got:\n%s", out)
	}
}

func TestEchoCLIInitPrefersDetectedPackageManager(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "my-app")
	out, exit := runEchoHelperProcess(t, root, []string{
		helperEnvKey + "=1",
		"npm_config_user_agent=pnpm/10.0.0 node/v22.0.0",
	}, "echo", "init", appDir)
	if exit != 0 {
		t.Fatalf("expected init to succeed, got exit %d:\n%s", exit, out)
	}
	if !strings.Contains(out, "pnpm install") {
		t.Fatalf("expected pnpm install hint, got:\n%s", out)
	}
}

func TestEchoCLIHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvKey) != "1" {
		return
	}

	sep := -1
	for i, arg := range os.Args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(os.Args) {
		fmt.Fprintln(os.Stderr, "missing helper args")
		os.Exit(2)
	}

	os.Args = os.Args[sep+1:]
	main()
	os.Exit(0)
}

func runEchoHelperProcess(t *testing.T, dir string, extraEnv []string, args ...string) (string, int) {
	t.Helper()

	cmdArgs := []string{"-test.run=TestEchoCLIHelperProcess", "--"}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	exit := 1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exit = exitErr.ExitCode()
	}
	return string(out), exit
}

func writeFakeNodeBinary(dir string) error {
	name := "node"
	content := "#!/bin/sh\nexit 0\n"
	mode := os.FileMode(0o755)

	if runtime.GOOS == "windows" {
		name = "node.cmd"
		content = "@echo off\r\nexit /b 0\r\n"
		mode = 0o644
	}

	path := filepath.Join(dir, name)
	return os.WriteFile(path, []byte(content), mode)
}

func mustContainFile(t *testing.T, path string, needle string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), needle) {
		t.Fatalf("%s missing %q", path, needle)
	}
}
