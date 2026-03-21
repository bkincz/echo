package loader

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Context struct {
	Params       map[string]string `json:"params"`
	SearchParams map[string]string `json:"searchParams"`
	Headers      map[string]string `json:"headers"`
}

type APIRequest struct {
	Method       string            `json:"method"`
	Params       map[string]string `json:"params"`
	SearchParams map[string]string `json:"searchParams"`
	Headers      map[string]string `json:"headers"`
	Body         string            `json:"body"`
}

type APIResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

const defaultRunnerTimeout = 10 * time.Second

type RunnerTimeouts struct {
	API    time.Duration
	Paths  time.Duration
	Loader time.Duration
}

type RunnerOptions struct {
	Timeouts RunnerTimeouts
	Logger   *slog.Logger
}

func DefaultRunnerTimeouts() RunnerTimeouts {
	return RunnerTimeouts{
		API:    defaultRunnerTimeout,
		Paths:  defaultRunnerTimeout,
		Loader: defaultRunnerTimeout,
	}
}

func normalizeRunnerTimeouts(in RunnerTimeouts) RunnerTimeouts {
	def := DefaultRunnerTimeouts()
	if in.API <= 0 {
		in.API = def.API
	}
	if in.Paths <= 0 {
		in.Paths = def.Paths
	}
	if in.Loader <= 0 {
		in.Loader = def.Loader
	}
	return in
}

// ---------------------------------------------------------------------------
// JS Worker — persistent line-protocol Node.js subprocess
// ---------------------------------------------------------------------------

type workerRequest struct {
	ID      string `json:"id"`
	Payload any    `json:"payload"`
}

type workerMessage struct {
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type workerResponse struct {
	data json.RawMessage
	err  error
}

type jsWorker struct {
	logger    *slog.Logger
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan workerResponse
	done      chan struct{}
	exitMu    sync.Mutex
	exit      error
	stopped   atomic.Bool
	nextID    atomic.Uint64
}

// startJSWorker launches `node -e <inlineScript> <modulePath>` as a persistent
// subprocess using a JSON line protocol: each request is a line written to
// stdin and each response is a line read from stdout.
func startJSWorker(logger *slog.Logger, inlineScript, modulePath string) (*jsWorker, error) {
	nodeBin, err := jsruntime.Require()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(nodeBin, "-e", inlineScript, modulePath) //nolint:gosec

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("worker stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("worker stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("worker stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start js worker: %w", err)
	}

	w := &jsWorker{
		logger:  logger,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan workerResponse),
		done:    make(chan struct{}),
	}

	go w.readStdout(stdout)
	go w.readStderr(stderr)

	go func() {
		err := cmd.Wait()
		w.setExit(err)
		if w.stopped.Load() {
			w.failAll(errors.New("js worker stopped"))
		} else if err != nil {
			w.failAll(fmt.Errorf("js worker exited: %w", err))
		} else {
			w.failAll(errors.New("js worker exited"))
		}
		close(w.done)
	}()

	return w, nil
}

// Call sends payload to the worker and blocks until it responds, the context
// expires, or the worker exits. timeout is applied on top of ctx so the
// stricter deadline wins.
func (w *jsWorker) Call(ctx context.Context, payload any, timeout time.Duration) (json.RawMessage, error) {
	if !w.Alive() {
		return nil, errors.New("js worker is not running")
	}

	id := strconv.FormatUint(w.nextID.Add(1), 10)
	raw, err := json.Marshal(workerRequest{ID: id, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("encode worker request: %w", err)
	}

	respCh := make(chan workerResponse, 1)
	w.registerPending(id, respCh)
	defer w.unregisterPending(id)

	w.writeMu.Lock()
	_, err = w.stdin.Write(append(raw, '\n'))
	w.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send worker request: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case resp := <-respCh:
		if resp.err != nil {
			return nil, resp.err
		}
		return resp.data, nil
	case <-w.done:
		if exitErr := w.exitError(); exitErr != nil {
			return nil, exitErr
		}
		return nil, errors.New("js worker exited")
	case <-callCtx.Done():
		return nil, callCtx.Err()
	}
}

// Stop closes stdin (signals EOF to the worker) and waits up to timeout for a
// clean exit before killing the process.
func (w *jsWorker) Stop(timeout time.Duration) error {
	if w.stopped.Swap(true) {
		return nil
	}
	_ = w.stdin.Close()
	select {
	case <-w.done:
	case <-time.After(timeout):
		if w.cmd != nil && w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
		<-w.done
	}
	return nil
}

func (w *jsWorker) Alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

func (w *jsWorker) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg workerMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			w.logger.Warn("js worker invalid response", "line", line, "err", err)
			continue
		}
		if msg.ID == "" {
			continue
		}
		ch := w.popPending(msg.ID)
		if ch == nil {
			continue
		}
		resp := workerResponse{data: msg.Data}
		if msg.Error != "" {
			resp.err = errors.New(msg.Error)
		}
		ch <- resp
		close(ch)
	}
	if err := scanner.Err(); err != nil && !w.stopped.Load() {
		w.logger.Warn("js worker stdout error", "err", err)
	}
}

func (w *jsWorker) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			w.logger.Warn("js worker stderr", "line", line)
		}
	}
}

func (w *jsWorker) setExit(err error) {
	w.exitMu.Lock()
	w.exit = err
	w.exitMu.Unlock()
}

func (w *jsWorker) exitError() error {
	w.exitMu.Lock()
	defer w.exitMu.Unlock()
	return w.exit
}

func (w *jsWorker) registerPending(id string, ch chan workerResponse) {
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
}

func (w *jsWorker) unregisterPending(id string) {
	w.pendingMu.Lock()
	delete(w.pending, id)
	w.pendingMu.Unlock()
}

func (w *jsWorker) popPending(id string) chan workerResponse {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	ch := w.pending[id]
	delete(w.pending, id)
	return ch
}

func (w *jsWorker) failAll(err error) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	for id, ch := range w.pending {
		delete(w.pending, id)
		ch <- workerResponse{err: err}
		close(ch)
	}
}

// ---------------------------------------------------------------------------
// Detection
// ---------------------------------------------------------------------------

var loaderExts = []string{".loader.ts", ".loader.tsx", ".loader.js", ".loader.jsx"}

func Find(pagesDir string) (map[string]string, error) {
	found := make(map[string]string)
	err := filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		for _, ext := range loaderExts {
			if strings.HasSuffix(name, ext) {
				rel, _ := filepath.Rel(pagesDir, path)
				key := filepath.ToSlash(strings.TrimSuffix(rel, ext))
				found[key] = filepath.ToSlash(filepath.Clean(path))
				break
			}
		}
		return nil
	})
	return found, err
}

func IsLoaderFile(path string) bool {
	for _, ext := range loaderExts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Loader — persistent worker for per-request data loading
// ---------------------------------------------------------------------------

// loaderWorkerScript is an inline CJS Node.js script that loads the compiled
// bundle once and then processes requests indefinitely via a JSON line protocol.
const loaderWorkerScript = `
const readline = require("readline");
const mod = require(process.argv[1]);
const fn = mod.loader;
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
(async () => {
  for await (const line of rl) {
    const t = line.trim();
    if (!t) continue;
    let id = "";
    try {
      const msg = JSON.parse(t);
      id = String(msg.id ?? "");
      if (!id) continue;
      const data = await Promise.resolve(typeof fn === "function" ? fn(msg.payload ?? {}) : null);
      process.stdout.write(JSON.stringify({ id, data: data ?? null }) + "\n");
    } catch (err) {
      if (id) {
        process.stdout.write(JSON.stringify({ id, error: err && err.message ? err.message : String(err) }) + "\n");
      } else {
        process.stderr.write(String(err) + "\n");
      }
    }
  }
})();
`

type Loader struct {
	scriptPath string
	timeouts   RunnerTimeouts
	logger     *slog.Logger

	workerMu sync.Mutex
	worker   *jsWorker
}

func (l *Loader) getWorker() (*jsWorker, error) {
	l.workerMu.Lock()
	defer l.workerMu.Unlock()

	if l.worker != nil && l.worker.Alive() {
		return l.worker, nil
	}
	if l.worker != nil {
		_ = l.worker.Stop(500 * time.Millisecond)
		l.worker = nil
	}

	w, err := startJSWorker(l.logger, loaderWorkerScript, l.scriptPath)
	if err != nil {
		return nil, err
	}
	l.worker = w
	return w, nil
}

func (l *Loader) Close() {
	l.workerMu.Lock()
	w := l.worker
	l.worker = nil
	l.workerMu.Unlock()

	if w != nil {
		_ = w.Stop(500 * time.Millisecond)
	}
	if err := os.Remove(l.scriptPath); err != nil && !os.IsNotExist(err) {
		l.logger.Warn("loader: remove script", "path", l.scriptPath, "err", err)
	}
}

func Build(appDir, filePath string) (*Loader, error) {
	return BuildWithOptions(appDir, filePath, RunnerOptions{})
}

func BuildWithOptions(appDir, filePath string, opts RunnerOptions) (*Loader, error) {
	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{filePath},
		Bundle:           true,
		Platform:         api.PlatformNode,
		Format:           api.FormatCommonJS,
		MinifyWhitespace: true,
		AbsWorkingDir:    appDir,
		Write:            false,
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("bundling loader %s: %s", filePath, result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return nil, fmt.Errorf("no output bundling loader %s", filePath)
	}

	tmp, err := os.CreateTemp("", "echo-loader-*.js")
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Write(result.OutputFiles[0].Contents); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Loader{
		scriptPath: tmp.Name(),
		timeouts:   normalizeRunnerTimeouts(opts.Timeouts),
		logger:     logger,
	}, nil
}

func (l *Loader) Run(ctx Context) (json.RawMessage, error) {
	return l.RunWithContext(context.Background(), ctx)
}

func (l *Loader) RunWithContext(parent context.Context, loaderCtx Context) (json.RawMessage, error) {
	w, err := l.getWorker()
	if err != nil {
		return nil, fmt.Errorf("loader worker: %w", err)
	}
	data, err := w.Call(parent, loaderCtx, l.timeouts.Loader)
	if err != nil {
		return nil, formatWorkerCallError(parent, err, "loader execution")
	}
	return data, nil
}

// ---------------------------------------------------------------------------
// Paths — process-per-call (called once at startup/build, not per request)
// ---------------------------------------------------------------------------

const pathsRunnerScript = `
const mod = require(process.argv[1]);
const fn = mod.paths;
Promise.resolve(typeof fn === 'function' ? fn() : [])
  .then(p => process.stdout.write(JSON.stringify(Array.isArray(p) ? p : [])))
  .catch(err => { process.stderr.write(err.message || String(err)); process.exit(1); });
`

func (l *Loader) Paths() ([]map[string]string, error) {
	return l.PathsWithContext(context.Background())
}

func (l *Loader) PathsWithContext(parent context.Context) ([]map[string]string, error) {
	rt, err := jsruntime.Require()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(parent, l.timeouts.Paths)
	defer cancel()

	cmd := exec.CommandContext(ctx, rt, "-e", pathsRunnerScript, l.scriptPath) //nolint:gosec
	out, err := cmd.Output()
	if err != nil {
		return nil, formatRunnerError(ctx, err, "paths() execution")
	}
	var paths []map[string]string
	if err := json.Unmarshal(out, &paths); err != nil {
		return nil, fmt.Errorf("decoding paths: %w", err)
	}
	return paths, nil
}

// ---------------------------------------------------------------------------
// APIRunner — persistent worker for JS API route handlers
// ---------------------------------------------------------------------------

// apiWorkerScript is an inline CJS Node.js script that loads the compiled API
// handler once and processes requests indefinitely via a JSON line protocol.
const apiWorkerScript = `
const readline = require("readline");
const mod = require(process.argv[1]);
const fn = mod.handler || mod.default;
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
(async () => {
  for await (const line of rl) {
    const t = line.trim();
    if (!t) continue;
    let id = "";
    try {
      const msg = JSON.parse(t);
      id = String(msg.id ?? "");
      if (!id) continue;
      const req = msg.payload ?? {};
      const res = await Promise.resolve(typeof fn === "function" ? fn(req) : null);
      let data;
      if (!res) {
        data = { status: 404, headers: {}, body: null };
      } else {
        const body = res.body !== undefined && typeof res.body !== "string"
          ? JSON.stringify(res.body) : (res.body ?? null);
        data = { status: res.status || 200, headers: res.headers || {}, body };
      }
      process.stdout.write(JSON.stringify({ id, data }) + "\n");
    } catch (err) {
      if (id) {
        process.stdout.write(JSON.stringify({ id, error: err && err.message ? err.message : String(err) }) + "\n");
      } else {
        process.stderr.write(String(err) + "\n");
      }
    }
  }
})();
`

type APIRunner struct {
	scriptPath string
	timeouts   RunnerTimeouts
	logger     *slog.Logger

	workerMu sync.Mutex
	worker   *jsWorker
}

func (a *APIRunner) getWorker() (*jsWorker, error) {
	a.workerMu.Lock()
	defer a.workerMu.Unlock()

	if a.worker != nil && a.worker.Alive() {
		return a.worker, nil
	}
	if a.worker != nil {
		_ = a.worker.Stop(500 * time.Millisecond)
		a.worker = nil
	}

	w, err := startJSWorker(a.logger, apiWorkerScript, a.scriptPath)
	if err != nil {
		return nil, err
	}
	a.worker = w
	return w, nil
}

func (a *APIRunner) Close() {
	a.workerMu.Lock()
	w := a.worker
	a.worker = nil
	a.workerMu.Unlock()

	if w != nil {
		_ = w.Stop(500 * time.Millisecond)
	}
	if err := os.Remove(a.scriptPath); err != nil && !os.IsNotExist(err) {
		a.logger.Warn("api runner: remove script", "path", a.scriptPath, "err", err)
	}
}

func BuildAPI(appDir, filePath string) (*APIRunner, error) {
	return BuildAPIWithOptions(appDir, filePath, RunnerOptions{})
}

func BuildAPIWithOptions(appDir, filePath string, opts RunnerOptions) (*APIRunner, error) {
	result := api.Build(api.BuildOptions{
		EntryPoints:      []string{filePath},
		Bundle:           true,
		Platform:         api.PlatformNode,
		Format:           api.FormatCommonJS,
		MinifyWhitespace: true,
		AbsWorkingDir:    appDir,
		Write:            false,
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("bundling api handler %s: %s", filePath, result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return nil, fmt.Errorf("no output bundling api handler %s", filePath)
	}

	tmp, err := os.CreateTemp("", "echo-api-*.js")
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Write(result.OutputFiles[0].Contents); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &APIRunner{
		scriptPath: tmp.Name(),
		timeouts:   normalizeRunnerTimeouts(opts.Timeouts),
		logger:     logger,
	}, nil
}

func (a *APIRunner) Run(req APIRequest) (APIResponse, error) {
	return a.RunWithContext(context.Background(), req)
}

func (a *APIRunner) RunWithContext(parent context.Context, req APIRequest) (APIResponse, error) {
	w, err := a.getWorker()
	if err != nil {
		return APIResponse{}, fmt.Errorf("api worker: %w", err)
	}
	raw, err := w.Call(parent, req, a.timeouts.API)
	if err != nil {
		return APIResponse{}, formatWorkerCallError(parent, err, "api handler execution")
	}
	var resp APIResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return APIResponse{}, fmt.Errorf("decoding api response: %w", err)
	}
	if resp.Status == 0 {
		resp.Status = 200
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Error helpers
// ---------------------------------------------------------------------------

func formatWorkerCallError(ctx context.Context, err error, op string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out", op)
	}
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s canceled", op)
	}
	return fmt.Errorf("%s failed: %w", op, err)
}

// formatRunnerError is kept for paths() which still uses process-per-call.
func formatRunnerError(ctx context.Context, err error, op string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out", op)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s canceled", op)
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			return fmt.Errorf("%s failed: %s", op, stderr)
		}
	}
	return fmt.Errorf("%s failed: %w", op, err)
}
