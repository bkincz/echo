package frontend

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"strings"
	"testing"
)

func TestFindSSREntry(t *testing.T) {
	t.Parallel()

	t.Run("finds src entry-server first", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		mustWrite(t, filepath.Join(dir, "src", "entry-server.tsx"))
		mustWrite(t, filepath.Join(dir, "entry.server.tsx"))

		got := findSSREntry(dir)
		if got != "src/entry-server.tsx" {
			t.Fatalf("got %q, want %q", got, "src/entry-server.tsx")
		}
	})

	t.Run("empty when no entry exists", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if got := findSSREntry(dir); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

func TestResolveViteExecutable(t *testing.T) {
	t.Parallel()

	t.Run("prefers local node_modules binary", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		bin := "vite"
		if runtime.GOOS == "windows" {
			bin = "vite.cmd"
		}
		local := filepath.Join(dir, "node_modules", ".bin", bin)
		mustWrite(t, local)

		got, err := resolveViteExecutable(dir)
		if err != nil {
			t.Fatalf("resolveViteExecutable: %v", err)
		}
		if filepath.Clean(got) != filepath.Clean(local) {
			t.Fatalf("got %q, want %q", got, local)
		}
	})
}

func newTestWorker() *viteRenderWorker {
	return &viteRenderWorker{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		pending: make(map[string]chan streamChunk),
		done:    make(chan struct{}),
	}
}

func readAllChunks(ch chan streamChunk) ([]string, error) {
	var out []string
	for sc := range ch {
		if sc.err != nil {
			return out, sc.err
		}
		if sc.done {
			return out, nil
		}
		out = append(out, sc.data)
	}
	return out, errors.New("channel closed without done sentinel")
}

func TestReadStdoutRoutesChunks(t *testing.T) {
	t.Parallel()

	w := newTestWorker()
	ch := make(chan streamChunk, 10)
	w.registerPending("1", ch)

	lines := `{"id":"1","chunk":"hello"}` + "\n" +
		`{"id":"1","chunk":" world"}` + "\n" +
		`{"id":"1","done":true}` + "\n"
	go w.readStdout(strings.NewReader(lines))

	chunks, err := readAllChunks(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 2 || chunks[0] != "hello" || chunks[1] != " world" {
		t.Fatalf("chunks = %v, want [hello  world]", chunks)
	}
}

func TestReadStdoutRoutesError(t *testing.T) {
	t.Parallel()

	w := newTestWorker()
	ch := make(chan streamChunk, 10)
	w.registerPending("2", ch)

	go w.readStdout(strings.NewReader(`{"id":"2","error":"boom"}` + "\n"))

	sc := <-ch
	if sc.err == nil || sc.err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", sc.err)
	}
}

func TestReadStdoutLegacyHTMLProtocol(t *testing.T) {
	t.Parallel()

	w := newTestWorker()
	ch := make(chan streamChunk, 10)
	w.registerPending("3", ch)

	go w.readStdout(strings.NewReader(`{"id":"3","html":"<html>legacy</html>"}` + "\n"))

	chunks, err := readAllChunks(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "<html>legacy</html>" {
		t.Fatalf("chunks = %v, want [<html>legacy</html>]", chunks)
	}
}

func TestReadStdoutIgnoresUnknownIDs(t *testing.T) {
	t.Parallel()

	w := newTestWorker()
	ch := make(chan streamChunk, 10)
	w.registerPending("known", ch)

	lines := `{"id":"unknown","chunk":"ignored"}` + "\n" +
		`{"id":"known","chunk":"ok"}` + "\n" +
		`{"id":"known","done":true}` + "\n"
	go w.readStdout(strings.NewReader(lines))

	chunks, err := readAllChunks(ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) != 1 || chunks[0] != "ok" {
		t.Fatalf("chunks = %v, want [ok]", chunks)
	}
}

func TestFailAllDeliverErrorToAllPending(t *testing.T) {
	t.Parallel()

	w := newTestWorker()
	ch1 := make(chan streamChunk, 2)
	ch2 := make(chan streamChunk, 2)
	w.registerPending("a", ch1)
	w.registerPending("b", ch2)

	want := errors.New("worker crashed")
	w.failAll(want)

	for _, ch := range []chan streamChunk{ch1, ch2} {
		sc, ok := <-ch
		if !ok {
			t.Fatal("channel closed before delivering error")
		}
		if sc.err != want {
			t.Fatalf("err = %v, want %v", sc.err, want)
		}
	}
}

// TestReadStdoutDoesNotBlockWhenConsumerCancelled verifies that readStdout
// does not leak its goroutine when the channel consumer stops draining (e.g.
// because ctx was cancelled) and the channel buffer fills up.
func TestReadStdoutDoesNotBlockWhenConsumerCancelled(t *testing.T) {
	t.Parallel()

	w := newTestWorker()

	// Buffer of 1 so it fills immediately; consumer never drains.
	ch := make(chan streamChunk, 1)
	w.registerPending("5", ch)
	// Remove the consumer right away to simulate a cancelled RenderStream.
	w.unregisterPending("5")

	// Send more chunks than the buffer holds and then a done sentinel.
	// If readStdout blocks, the goroutine below hangs and the test times out.
	lines := `{"id":"5","chunk":"a"}` + "\n" +
		`{"id":"5","chunk":"b"}` + "\n" +
		`{"id":"5","chunk":"c"}` + "\n" +
		`{"id":"5","done":true}` + "\n"

	done := make(chan struct{})
	go func() {
		w.readStdout(strings.NewReader(lines))
		close(done)
	}()

	select {
	case <-done:
		// readStdout returned without blocking — correct.
	case <-time.After(2 * time.Second):
		t.Fatal("readStdout goroutine leaked — blocked on full channel after consumer cancelled")
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
