package nodeproc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Runner struct {
	logger *slog.Logger
}

type Process struct {
	cmd    *exec.Cmd
	logger *slog.Logger
	name   string
	done   chan error
	once   sync.Once
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewRunner(logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{logger: logger}
}

// ---------------------------------------------------------------------------
// Run
// ---------------------------------------------------------------------------

func (r *Runner) Run(ctx context.Context, dir, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return fmt.Errorf("run %s %s: %w", bin, strings.Join(args, " "), err)
		}
		return fmt.Errorf("run %s %s: %w\n%s", bin, strings.Join(args, " "), err, trimmed)
	}
	return nil
}

func (r *Runner) Start(ctx context.Context, dir, bin string, args ...string) (*Process, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s %s: %w", bin, strings.Join(args, " "), err)
	}

	proc := &Process{
		cmd:    cmd,
		logger: r.logger,
		name:   bin,
		done:   make(chan error, 1),
	}

	go streamLines(stdout, func(line string) {
		proc.logger.Info("process stdout", "proc", proc.name, "line", line)
	})
	go streamLines(stderr, func(line string) {
		proc.logger.Warn("process stderr", "proc", proc.name, "line", line)
	})

	go func() {
		proc.done <- cmd.Wait()
		close(proc.done)
	}()

	return proc, nil
}

// ---------------------------------------------------------------------------
// Process
// ---------------------------------------------------------------------------

func (p *Process) Wait() error {
	err, ok := <-p.done
	if !ok {
		return nil
	}
	return err
}

func (p *Process) Stop(timeout time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	var stopErr error
	p.once.Do(func() {
		if err := p.cmd.Process.Signal(os.Interrupt); err != nil {
			if !errorsIsProcessDone(err) {
				stopErr = err
			}
		}

		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case err, ok := <-p.done:
			if ok && err != nil && !errorsIsProcessDone(err) {
				stopErr = err
			}
		case <-timer.C:
			_ = p.cmd.Process.Kill()
			err, ok := <-p.done
			if ok && err != nil && !errorsIsProcessDone(err) {
				stopErr = err
			}
		}
	})

	if stopErr != nil {
		return fmt.Errorf("stop %s: %w", p.name, stopErr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

func streamLines(r io.Reader, fn func(line string)) {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		fn(line)
	}
}

func errorsIsProcessDone(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrProcessDone) {
		return true
	}
	if strings.Contains(err.Error(), "process already finished") {
		return true
	}
	return false
}
