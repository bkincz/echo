package server

import (
	"path/filepath"
	"testing"
)

func createSmokeApp(t *testing.T) string {
	t.Helper()

	appDir := t.TempDir()
	src := filepath.Join("testdata", "smoke-app")
	if err := copyDir(src, appDir); err != nil {
		t.Fatalf("copy smoke fixture: %v", err)
	}
	return appDir
}
