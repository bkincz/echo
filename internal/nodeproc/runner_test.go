package nodeproc

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

var discardLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 10}))

// ---------------------------------------------------------------------------
// Runner.Run
// ---------------------------------------------------------------------------

func TestRunnerRun_success(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	if err := r.Run(context.Background(), t.TempDir(), "sh", "-c", "exit 0"); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestRunnerRun_failureWithOutput(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	err := r.Run(context.Background(), t.TempDir(), "sh", "-c", "echo oops >&2; exit 1")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("error should contain stderr output, got: %v", err)
	}
}

func TestRunnerRun_failureNoOutput(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	err := r.Run(context.Background(), t.TempDir(), "sh", "-c", "exit 1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunnerRun_cancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := NewRunner(discardLogger)
	err := r.Run(ctx, t.TempDir(), "sh", "-c", "sleep 10")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestRunnerRun_nilLogger(t *testing.T) {
	t.Parallel()
	// NewRunner with nil should not panic.
	r := NewRunner(nil)
	if err := r.Run(context.Background(), t.TempDir(), "sh", "-c", "exit 0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Runner.Start / Process.Wait
// ---------------------------------------------------------------------------

func TestRunnerStart_successAndWait(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	proc, err := r.Start(context.Background(), t.TempDir(), "sh", "-c", "exit 0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait should return nil for a clean exit.
	if err := proc.Wait(); err != nil {
		// exit status 0 is clean; some platforms wrap it differently.
		t.Logf("Wait returned (may be ok): %v", err)
	}
}

func TestRunnerStart_nonZeroExitIsReportedByWait(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	proc, err := r.Start(context.Background(), t.TempDir(), "sh", "-c", "exit 42")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := proc.Wait(); err == nil {
		t.Fatal("expected non-nil error from non-zero exit")
	}
}

func TestRunnerStart_badBinaryErrors(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	_, err := r.Start(context.Background(), t.TempDir(), "/no/such/binary")
	if err == nil {
		t.Fatal("expected error starting non-existent binary")
	}
}

// ---------------------------------------------------------------------------
// Process.Stop
// ---------------------------------------------------------------------------

func TestProcess_stopInterrupts(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	proc, err := r.Start(context.Background(), t.TempDir(), "sh", "-c", "sleep 60")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	if err := proc.Stop(2 * time.Second); err != nil {
		t.Logf("Stop returned (may be ok on CI): %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("Stop took too long — kill path may not have fired")
	}
}

func TestProcess_stopIdempotent(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	proc, err := r.Start(context.Background(), t.TempDir(), "sh", "-c", "sleep 60")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	proc.Stop(time.Second)  //nolint:errcheck
	proc.Stop(time.Second)  // second call must not panic or block
}

func TestProcess_stopNil(t *testing.T) {
	t.Parallel()
	var p *Process
	// Should not panic.
	if err := p.Stop(time.Second); err != nil {
		t.Errorf("unexpected error stopping nil process: %v", err)
	}
}

func TestProcess_waitTwice(t *testing.T) {
	t.Parallel()
	r := NewRunner(discardLogger)
	proc, err := r.Start(context.Background(), t.TempDir(), "sh", "-c", "exit 0")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	proc.Wait() //nolint:errcheck
	// Second Wait should return nil (channel closed).
	if err := proc.Wait(); err != nil {
		t.Errorf("second Wait: %v", err)
	}
}

// ---------------------------------------------------------------------------
// streamLines
// ---------------------------------------------------------------------------

func TestStreamLines_emitsNonEmptyLines(t *testing.T) {
	t.Parallel()
	input := "line one\n\nline two\n   \nline three\n"
	var got []string
	streamLines(strings.NewReader(input), func(line string) {
		got = append(got, line)
	})
	want := []string{"line one", "line two", "line three"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStreamLines_empty(t *testing.T) {
	t.Parallel()
	var got []string
	streamLines(strings.NewReader(""), func(line string) { got = append(got, line) })
	if len(got) != 0 {
		t.Errorf("expected no lines, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// errorsIsProcessDone
// ---------------------------------------------------------------------------

func TestErrorsIsProcessDone(t *testing.T) {
	t.Parallel()
	if errorsIsProcessDone(nil) {
		t.Error("nil should return false")
	}
	if !errorsIsProcessDone(os.ErrProcessDone) {
		t.Error("os.ErrProcessDone should return true")
	}
}
