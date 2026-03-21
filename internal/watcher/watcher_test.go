package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// collect gathers onChange callbacks until the channel is closed or timeout.
func collect(t *testing.T, ch <-chan []string, timeout time.Duration) []string {
	t.Helper()
	select {
	case paths := <-ch:
		return paths
	case <-time.After(timeout):
		t.Fatal("timed out waiting for onChange callback")
		return nil
	}
}

func newWatcher(t *testing.T, dir string) (*Watcher, chan []string) {
	t.Helper()
	ch := make(chan []string, 4)
	w, err := New(func(paths []string) { ch <- paths }, dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(w.Close)
	w.Start()
	return w, ch
}

func TestWatcherDebounce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, ch := newWatcher(t, dir)

	f := filepath.Join(dir, "file.txt")
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// All 5 writes should be collapsed into a single callback.
	paths := collect(t, ch, 500*time.Millisecond)
	if len(paths) == 0 {
		t.Fatal("expected at least one path in callback")
	}

	// No second callback should arrive quickly.
	select {
	case extra := <-ch:
		t.Errorf("unexpected second callback: %v", extra)
	case <-time.After(200 * time.Millisecond):
		// good
	}
}

func TestWatcherBatchesPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, ch := newWatcher(t, dir)

	// Write two different files within a single debounce window.
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Both paths should arrive in a single sorted callback.
	paths := collect(t, ch, 500*time.Millisecond)
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
	}
	if !seen[a] || !seen[b] {
		t.Errorf("expected both files in batch, got %v", paths)
	}
	// Verify sorted order.
	for i := 1; i < len(paths); i++ {
		if paths[i] < paths[i-1] {
			t.Errorf("paths not sorted: %v", paths)
		}
	}
}

func TestWatcherSkipsDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Create the skipped dirs before starting the watcher.
	for _, name := range []string{"node_modules", "dist", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, ch := newWatcher(t, dir)

	// Write inside each skipped dir; the watcher should not have added them.
	// Even if events are emitted by the OS, we just verify our addRecursive
	// didn't register those dirs — so no callback is expected.
	for _, name := range []string{"node_modules", "dist", ".git"} {
		_ = os.WriteFile(filepath.Join(dir, name, "x.txt"), []byte("x"), 0o644)
	}

	select {
	case got := <-ch:
		// Only fail if the path is inside one of the skipped dirs.
		for _, p := range got {
			for _, skip := range []string{"node_modules", "dist", ".git"} {
				if filepath.Base(filepath.Dir(p)) == skip {
					t.Errorf("received event inside skipped dir %q: %v", skip, got)
				}
			}
		}
	case <-time.After(300 * time.Millisecond):
		// No callback for skipped dirs — expected.
	}
}

func TestWatcherNewSubdirIsWatched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, ch := newWatcher(t, dir)

	// Create a new subdirectory; the watcher should auto-register it.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Drain the Create event for the directory itself.
	collect(t, ch, 500*time.Millisecond)

	// Now write inside the new subdir — should be watched.
	f := filepath.Join(sub, "file.go")
	if err := os.WriteFile(f, []byte("pkg"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := collect(t, ch, 500*time.Millisecond)
	found := false
	for _, p := range paths {
		if p == f {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %s in callback paths, got %v", f, paths)
	}
}

func TestWatcherSyncDirsAddsAndRemoves(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}

	ch := make(chan []string, 4)
	w, err := New(func(paths []string) { ch <- paths })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(w.Close)
	w.Start()

	// Add dir a via SyncDirs.
	if err := w.SyncDirs([]string{a}); err != nil {
		t.Fatalf("SyncDirs add: %v", err)
	}

	if err := os.WriteFile(filepath.Join(a, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	collect(t, ch, 500*time.Millisecond) // a is watched

	// Swap to dir b — a should be unwatched.
	if err := w.SyncDirs([]string{b}); err != nil {
		t.Fatalf("SyncDirs swap: %v", err)
	}

	// Write to a — should not trigger.
	if err := os.WriteFile(filepath.Join(a, "f2.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write to b — should trigger.
	if err := os.WriteFile(filepath.Join(b, "f.txt"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Collect what we get; it must include b's file and must not include a's file.
	var mu sync.Mutex
	var collected []string
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case paths := <-ch:
			mu.Lock()
			collected = append(collected, paths...)
			mu.Unlock()
		case <-deadline:
			break loop
		}
	}

	foundB := false
	for _, p := range collected {
		if p == filepath.Join(b, "f.txt") {
			foundB = true
		}
		if p == filepath.Join(a, "f2.txt") {
			t.Errorf("received event for removed dir a: %v", collected)
		}
	}
	if !foundB {
		t.Errorf("expected event for dir b, got %v", collected)
	}
}

func TestWatcherClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, _ := newWatcher(t, dir)
	// Second close should not panic.
	w.Close()
	w.Close()
}

func TestAddRecursiveNonExistentDir(t *testing.T) {
	t.Parallel()
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	// Should silently succeed for a non-existent directory.
	if err := addRecursive(fw, filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Errorf("addRecursive non-existent: %v", err)
	}
}

func TestAddRecursiveFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer fw.Close()
	// Passing a file (not a dir) should silently succeed.
	if err := addRecursive(fw, f); err != nil {
		t.Errorf("addRecursive file: %v", err)
	}
}
