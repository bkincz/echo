package frontend

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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/echo-ssr/echo/internal/jsruntime"
	"github.com/echo-ssr/echo/internal/nodeproc"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type ViteOptions struct {
	Logger *slog.Logger
	Runner *nodeproc.Runner
	// Workers is the number of concurrent SSR render worker processes.
	// Each worker is an independent Node.js process so renders execute in
	// parallel across all CPU cores. Defaults to runtime.NumCPU() when 0.
	Workers int
}

type ViteEngine struct {
	logger  *slog.Logger
	runner  *nodeproc.Runner
	workers int

	mu      sync.Mutex
	pool    []*viteRenderWorker
	poolKey string // appDir+"\x00"+entry+"\x00"+mode; rebuilt when this changes
}

type viteRenderWorker struct {
	logger *slog.Logger
	appDir string
	entry  string
	mode   string

	cmd   *exec.Cmd
	stdin io.WriteCloser

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]chan streamChunk

	done   chan struct{}
	exitMu sync.Mutex
	exit   error

	stopped atomic.Bool
	nextID  atomic.Uint64
}

// streamChunk is a single unit of streaming render output.
type streamChunk struct {
	data string
	done bool
	err  error
}

type workerRequest struct {
	ID      string         `json:"id"`
	Payload map[string]any `json:"payload"`
}

type workerMessage struct {
	ID    string `json:"id"`
	// Streaming protocol (preferred)
	Chunk string `json:"chunk,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Error string `json:"error,omitempty"`
	// Legacy single-shot protocol (entry exports render, not renderStream)
	HTML string `json:"html,omitempty"`
}

// flusher mirrors http.Flusher to avoid importing net/http.
type flusher interface{ Flush() }

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewViteEngine(opts ViteOptions) *ViteEngine {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	runner := opts.Runner
	if runner == nil {
		runner = nodeproc.NewRunner(logger)
	}

	return &ViteEngine{logger: logger, runner: runner, workers: opts.Workers}
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

func (v *ViteEngine) Name() string {
	return "vite"
}

func (v *ViteEngine) Validate(appDir string) error {
	if _, err := jsruntime.Require(); err != nil {
		return err
	}
	_, err := resolveViteExecutable(appDir)
	if err != nil {
		return err
	}
	return nil
}

func (v *ViteEngine) StartDev(ctx context.Context, appDir string, opts DevOptions) (*nodeproc.Process, error) {
	bin, err := resolveViteExecutable(appDir)
	if err != nil {
		return nil, err
	}

	host := opts.Host
	if host == "" {
		host = "localhost"
	}
	port := opts.Port
	if port <= 0 {
		port = 5173
	}

	args := []string{"--host", host, "--port", strconv.Itoa(port), "--strictPort", "--clearScreen", "false"}
	v.logger.Info("frontend engine", "engine", v.Name(), "mode", "dev", "host", host, "port", port)

	proc, err := v.runner.Start(ctx, appDir, bin, args...)
	if err != nil {
		return nil, err
	}
	return proc, nil
}

func (v *ViteEngine) BuildClient(ctx context.Context, appDir string, opts BuildClientOptions) error {
	bin, err := resolveViteExecutable(appDir)
	if err != nil {
		return err
	}

	args := []string{"build"}
	if opts.OutDir != "" {
		args = append(args, "--outDir", opts.OutDir)
	}
	v.logger.Info("frontend engine", "engine", v.Name(), "mode", "build-client")
	return v.runner.Run(ctx, appDir, bin, args...)
}

func (v *ViteEngine) BuildServer(ctx context.Context, appDir string, opts BuildServerOptions) error {
	bin, err := resolveViteExecutable(appDir)
	if err != nil {
		return err
	}

	entry := opts.SSREntry
	if entry == "" {
		entry = findSSREntry(appDir)
	}
	if entry == "" {
		return fmt.Errorf("vite ssr entry not found (expected one of: src/entry-server.tsx, src/entry.server.tsx, entry-server.tsx, entry.server.tsx)")
	}

	args := []string{"build", "--ssr", entry}
	if opts.OutDir != "" {
		args = append(args, "--outDir", opts.OutDir)
	}
	v.logger.Info("frontend engine", "engine", v.Name(), "mode", "build-server", "entry", entry)
	return v.runner.Run(ctx, appDir, bin, args...)
}

func (v *ViteEngine) Render(ctx context.Context, appDir string, opts RenderOptions) (RenderResult, error) {
	if _, err := jsruntime.Require(); err != nil {
		return RenderResult{}, err
	}

	entry := opts.SSREntry
	if entry == "" {
		entry = findSSREntry(appDir)
	}
	if entry == "" {
		return RenderResult{}, fmt.Errorf("vite ssr entry not found (expected one of: src/entry-server.tsx, src/entry.server.tsx, entry-server.tsx, entry.server.tsx)")
	}

	mode := "dev"
	if isBuiltSSREntry(appDir, entry) {
		mode = "prod"
	} else {
		if _, err := resolveViteExecutable(appDir); err != nil {
			return RenderResult{}, err
		}
	}

	payload := map[string]any{
		"url":          opts.URL,
		"routePattern": opts.RoutePattern,
		"status":       opts.Status,
		"shell":        opts.Shell,
	}
	if opts.LoaderData != nil {
		var loaderData any
		if err := json.Unmarshal(opts.LoaderData, &loaderData); err != nil {
			return RenderResult{}, fmt.Errorf("invalid loader data json: %w", err)
		}
		payload["loaderData"] = loaderData
	}

	worker, err := v.pickWorker(appDir, entry, mode)
	if err != nil {
		return RenderResult{}, err
	}
	return worker.Render(ctx, payload)
}

func (v *ViteEngine) RenderStream(ctx context.Context, appDir string, opts RenderOptions, w io.Writer) error {
	if _, err := jsruntime.Require(); err != nil {
		return err
	}

	entry := opts.SSREntry
	if entry == "" {
		entry = findSSREntry(appDir)
	}
	if entry == "" {
		return fmt.Errorf("vite ssr entry not found (expected one of: src/entry-server.tsx, src/entry.server.tsx, entry-server.tsx, entry.server.tsx)")
	}

	mode := "dev"
	if isBuiltSSREntry(appDir, entry) {
		mode = "prod"
	} else {
		if _, err := resolveViteExecutable(appDir); err != nil {
			return err
		}
	}

	payload := map[string]any{
		"url":          opts.URL,
		"routePattern": opts.RoutePattern,
		"status":       opts.Status,
		"shell":        opts.Shell,
	}
	if opts.LoaderData != nil {
		var loaderData any
		if err := json.Unmarshal(opts.LoaderData, &loaderData); err != nil {
			return fmt.Errorf("invalid loader data json: %w", err)
		}
		payload["loaderData"] = loaderData
	}

	worker, err := v.pickWorker(appDir, entry, mode)
	if err != nil {
		return err
	}
	return worker.RenderStream(ctx, payload, w)
}

func (v *ViteEngine) Close() error {
	v.mu.Lock()
	pool := v.pool
	v.pool = nil
	v.poolKey = ""
	v.mu.Unlock()

	var errs []error
	for _, w := range pool {
		if err := w.Stop(2 * time.Second); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

func (v *ViteEngine) pickWorker(appDir, entry, mode string) (*viteRenderWorker, error) {
	normalizedAppDir := filepath.Clean(appDir)
	normalizedEntry := filepath.ToSlash(entry)

	v.mu.Lock()
	defer v.mu.Unlock()

	poolKey := normalizedAppDir + "\x00" + normalizedEntry + "\x00" + mode
	if poolKey != v.poolKey {
		for _, w := range v.pool {
			_ = w.Stop(2 * time.Second)
		}
		v.pool = nil
		v.poolKey = ""
	}

	alive := v.pool[:0]
	for _, w := range v.pool {
		if w.Alive() {
			alive = append(alive, w)
		}
	}
	v.pool = alive

	target := v.workers
	if target <= 0 {
		target = runtime.NumCPU()
	}
	for len(v.pool) < target {
		w, err := startViteRenderWorker(v.logger, normalizedAppDir, normalizedEntry, mode)
		if err != nil {
			if len(v.pool) == 0 {
				return nil, err
			}
			break // partial pool — work with what we have
		}
		v.pool = append(v.pool, w)
	}
	v.poolKey = poolKey

	var best *viteRenderWorker
	bestPending := -1
	for _, w := range v.pool {
		if n := w.pendingCount(); bestPending < 0 || n < bestPending {
			best = w
			bestPending = n
		}
	}
	if best == nil {
		return nil, errors.New("no vite render workers available")
	}
	return best, nil
}

func startViteRenderWorker(logger *slog.Logger, appDir, entry, mode string) (*viteRenderWorker, error) {
	scriptPath, cleanup, err := writeRenderWorkerScript(mode == "dev")
	if err != nil {
		return nil, err
	}

	nodeBin, err := exec.LookPath("node")
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("node lookup failed: %w", err)
	}

	cmd := exec.Command(nodeBin, scriptPath, "--root", appDir, "--entry", entry)
	cmd.Dir = appDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("worker stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("worker stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("worker stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start vite render worker: %w", err)
	}

	w := &viteRenderWorker{
		logger:  logger,
		appDir:  appDir,
		entry:   entry,
		mode:    mode,
		cmd:     cmd,
		stdin:   stdin,
		pending: make(map[string]chan streamChunk),
		done:    make(chan struct{}),
	}

	go w.readStdout(stdout)
	go w.readStderr(stderr)

	go func() {
		err := cmd.Wait()
		cleanup()
		w.setExit(err)
		if w.stopped.Load() {
			w.failAll(errors.New("vite render worker stopped"))
		} else if err != nil {
			w.failAll(fmt.Errorf("vite render worker exited: %w", err))
		} else {
			w.failAll(errors.New("vite render worker exited"))
		}
		close(w.done)
	}()

	logger.Info("frontend render worker started", "engine", "vite", "entry", entry, "mode", mode)
	return w, nil
}

func (w *viteRenderWorker) RenderStream(ctx context.Context, payload map[string]any, out io.Writer) error {
	if !w.Alive() {
		return fmt.Errorf("vite render worker is not running")
	}

	id := strconv.FormatUint(w.nextID.Add(1), 10)
	req := workerRequest{ID: id, Payload: payload}
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode render request: %w", err)
	}

	ch := make(chan streamChunk, 64)
	w.registerPending(id, ch)
	defer w.unregisterPending(id)

	w.writeMu.Lock()
	_, err = w.stdin.Write(append(raw, '\n'))
	w.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("send render request: %w", err)
	}

	for {
		select {
		case sc, ok := <-ch:
			if !ok {
				return errors.New("render stream channel closed unexpectedly")
			}
			if sc.err != nil {
				return sc.err
			}
			if sc.done {
				return nil
			}
			if _, werr := io.WriteString(out, sc.data); werr != nil {
				return fmt.Errorf("write chunk: %w", werr)
			}
			if f, ok := out.(flusher); ok {
				f.Flush()
			}
		case <-w.done:
			exitErr := w.exitError()
			if exitErr == nil {
				exitErr = errors.New("vite render worker exited")
			}
			return exitErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (w *viteRenderWorker) Render(ctx context.Context, payload map[string]any) (RenderResult, error) {
	var buf strings.Builder
	if err := w.RenderStream(ctx, payload, &buf); err != nil {
		return RenderResult{}, err
	}
	html := buf.String()
	if strings.TrimSpace(html) == "" {
		return RenderResult{}, fmt.Errorf("vite render returned empty html")
	}
	return RenderResult{HTML: html}, nil
}

func (w *viteRenderWorker) Stop(timeout time.Duration) error {
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

func (w *viteRenderWorker) Matches(appDir, entry, mode string) bool {
	return filepath.Clean(w.appDir) == filepath.Clean(appDir) &&
		filepath.ToSlash(w.entry) == filepath.ToSlash(entry) &&
		w.mode == mode
}

func (w *viteRenderWorker) pendingCount() int {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	return len(w.pending)
}

func (w *viteRenderWorker) Alive() bool {
	select {
	case <-w.done:
		return false
	default:
		return true
	}
}

func (w *viteRenderWorker) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var msg workerMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			w.logger.Warn("frontend render worker invalid response", "line", line, "err", err)
			continue
		}
		if msg.ID == "" {
			continue
		}

		switch {
		case msg.Error != "":
			if ch := w.popPending(msg.ID); ch != nil {
				ch <- streamChunk{err: errors.New(msg.Error)}
				close(ch)
			}
		case msg.Done:
			if ch := w.popPending(msg.ID); ch != nil {
				ch <- streamChunk{done: true}
				close(ch)
			}
		case msg.Chunk != "":
			w.pendingMu.Lock()
			ch := w.pending[msg.ID]
			w.pendingMu.Unlock()
			if ch != nil {
				select {
				case ch <- streamChunk{data: msg.Chunk}:
				case <-w.done:
				}
			}
		case msg.HTML != "":
			if ch := w.popPending(msg.ID); ch != nil {
				ch <- streamChunk{data: msg.HTML}
				ch <- streamChunk{done: true}
				close(ch)
			}
		}
	}

	if err := scanner.Err(); err != nil && !w.stopped.Load() {
		w.logger.Warn("frontend render worker stdout error", "err", err)
	}
}

func (w *viteRenderWorker) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		w.logger.Warn("frontend render worker stderr", "line", line)
	}
}

func (w *viteRenderWorker) setExit(err error) {
	w.exitMu.Lock()
	w.exit = err
	w.exitMu.Unlock()
}

func (w *viteRenderWorker) exitError() error {
	w.exitMu.Lock()
	defer w.exitMu.Unlock()
	return w.exit
}

func (w *viteRenderWorker) registerPending(id string, ch chan streamChunk) {
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
}

func (w *viteRenderWorker) unregisterPending(id string) {
	w.pendingMu.Lock()
	delete(w.pending, id)
	w.pendingMu.Unlock()
}

func (w *viteRenderWorker) popPending(id string) chan streamChunk {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	ch := w.pending[id]
	delete(w.pending, id)
	return ch
}

func (w *viteRenderWorker) failAll(err error) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	for id, ch := range w.pending {
		delete(w.pending, id)
		ch <- streamChunk{err: err}
		close(ch)
	}
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

func resolveViteExecutable(appDir string) (string, error) {
	viteBin := "vite"
	if runtime.GOOS == "windows" {
		viteBin = "vite.cmd"
	}

	local := filepath.Join(appDir, "node_modules", ".bin", viteBin)
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return local, nil
	}

	global, err := exec.LookPath("vite")
	if err == nil {
		return global, nil
	}

	return "", fmt.Errorf("vite not found. install it in app dependencies (npm i -D vite) or ensure it is available in PATH")
}

func findSSREntry(appDir string) string {
	candidates := []string{
		"src/entry-server.tsx",
		"src/entry.server.tsx",
		"entry-server.tsx",
		"entry.server.tsx",
	}
	for _, rel := range candidates {
		abs := filepath.Join(appDir, rel)
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			return rel
		}
	}
	return ""
}

func isBuiltSSREntry(appDir, entry string) bool {
	if entry == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(entry))
	if ext != ".js" && ext != ".mjs" && ext != ".cjs" {
		return false
	}
	abs := entry
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(appDir, abs)
	}
	info, err := os.Stat(abs)
	return err == nil && !info.IsDir()
}

func writeRenderWorkerScript(useVite bool) (string, func(), error) {
	tmp, err := os.CreateTemp("", "echo-vite-render-worker-*.mjs")
	if err != nil {
		return "", nil, fmt.Errorf("create vite render worker script: %w", err)
	}
	source := viteDevRenderWorkerScript
	if !useVite {
		source = viteProdRenderWorkerScript
	}
	if _, err := tmp.WriteString(source); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("write vite render worker script: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", nil, fmt.Errorf("close vite render worker script: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(tmp.Name())
	}
	return tmp.Name(), cleanup, nil
}

const viteDevRenderWorkerScript = `import readline from "node:readline";
import { createServer } from "vite";

function parseArgs(argv) {
  const out = { root: "", entry: "" };
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i];
    const value = argv[i + 1] ?? "";
    if (key === "--root") out.root = value;
    if (key === "--entry") out.entry = value;
    if (key.startsWith("--")) i++;
  }
  return out;
}

function stringifyError(err) {
  if (!err) return "Unknown render error";
  if (typeof err === "string") return err;
  if (err.stack) return String(err.stack);
  if (err.message) return String(err.message);
  return String(err);
}

function buildCtx(payload) {
  return {
    url: payload.url || "/",
    routePattern: payload.routePattern || "",
    status: Number(payload.status || 200),
    shell: payload.shell || "",
    loaderData: Object.prototype.hasOwnProperty.call(payload, "loaderData") ? payload.loaderData : null,
  };
}

try {
  const args = parseArgs(process.argv.slice(2));
  if (!args.root) throw new Error("missing --root");
  if (!args.entry) throw new Error("missing --entry");

  const entryPath = String(args.entry).startsWith("/") ? String(args.entry) : "/" + String(args.entry);

  const vite = await createServer({
    root: args.root,
    logLevel: "error",
    appType: "custom",
    server: { middlewareMode: true },
  });

  const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

  for await (const line of rl) {
    if (!line || !line.trim()) continue;

    let id = "";
    try {
      const msg = JSON.parse(line);
      id = String(msg.id || "");
      if (!id) continue;
      const payload = msg.payload || {};
      const ctx = buildCtx(payload);

      const mod = await vite.ssrLoadModule(entryPath);

      if (typeof mod.renderStream === "function") {
        // Streaming path: entry exports renderStream(ctx) -> Node.js Readable
        const readable = await mod.renderStream(ctx);
        await new Promise((resolve, reject) => {
          readable.on("data", (chunk) => {
            process.stdout.write(JSON.stringify({ id, chunk: chunk.toString("utf8") }) + "\n");
          });
          readable.on("end", () => {
            process.stdout.write(JSON.stringify({ id, done: true }) + "\n");
            resolve(undefined);
          });
          readable.on("error", (err) => {
            process.stdout.write(JSON.stringify({ id, error: stringifyError(err) }) + "\n");
            reject(err);
          });
        });
      } else {
        // Legacy path: entry exports render(ctx) -> Promise<string>
        const render =
          typeof mod.render === "function" ? mod.render
          : typeof mod.default === "function" ? mod.default
          : mod.default && typeof mod.default.render === "function" ? mod.default.render
          : null;
        if (!render) throw new Error("SSR entry must export renderStream(ctx) or render(ctx)");

        const rendered = await render(ctx);
        const html =
          typeof rendered === "string" ? rendered
          : rendered && typeof rendered.html === "string" ? rendered.html
          : "";
        if (!html) throw new Error("SSR render returned empty HTML");
        process.stdout.write(JSON.stringify({ id, chunk: html }) + "\n");
        process.stdout.write(JSON.stringify({ id, done: true }) + "\n");
      }
    } catch (err) {
      if (id) {
        process.stdout.write(JSON.stringify({ id, error: stringifyError(err) }) + "\n");
      } else {
        console.error(stringifyError(err));
      }
    }
  }

  await vite.close();
} catch (err) {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
}
`

const viteProdRenderWorkerScript = `import readline from "node:readline";
import path from "node:path";
import { pathToFileURL } from "node:url";

function parseArgs(argv) {
  const out = { root: "", entry: "" };
  for (let i = 0; i < argv.length; i++) {
    const key = argv[i];
    const value = argv[i + 1] ?? "";
    if (key === "--root") out.root = value;
    if (key === "--entry") out.entry = value;
    if (key.startsWith("--")) i++;
  }
  return out;
}

function stringifyError(err) {
  if (!err) return "Unknown render error";
  if (typeof err === "string") return err;
  if (err.stack) return String(err.stack);
  if (err.message) return String(err.message);
  return String(err);
}

function buildCtx(payload) {
  return {
    url: payload.url || "/",
    routePattern: payload.routePattern || "",
    status: Number(payload.status || 200),
    shell: payload.shell || "",
    loaderData: Object.prototype.hasOwnProperty.call(payload, "loaderData") ? payload.loaderData : null,
  };
}

try {
  const args = parseArgs(process.argv.slice(2));
  if (!args.entry) throw new Error("missing --entry");

  const root = args.root || process.cwd();
  const entryAbs = path.isAbsolute(args.entry) ? args.entry : path.resolve(root, args.entry);
  const mod = await import(pathToFileURL(entryAbs).href);

  const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

  for await (const line of rl) {
    if (!line || !line.trim()) continue;

    let id = "";
    try {
      const msg = JSON.parse(line);
      id = String(msg.id || "");
      if (!id) continue;
      const payload = msg.payload || {};
      const ctx = buildCtx(payload);

      if (typeof mod.renderStream === "function") {
        // Streaming path
        const readable = await mod.renderStream(ctx);
        await new Promise((resolve, reject) => {
          readable.on("data", (chunk) => {
            process.stdout.write(JSON.stringify({ id, chunk: chunk.toString("utf8") }) + "\n");
          });
          readable.on("end", () => {
            process.stdout.write(JSON.stringify({ id, done: true }) + "\n");
            resolve(undefined);
          });
          readable.on("error", (err) => {
            process.stdout.write(JSON.stringify({ id, error: stringifyError(err) }) + "\n");
            reject(err);
          });
        });
      } else {
        // Legacy path
        const render =
          typeof mod.render === "function" ? mod.render
          : typeof mod.default === "function" ? mod.default
          : mod.default && typeof mod.default.render === "function" ? mod.default.render
          : null;
        if (!render) throw new Error("SSR entry must export renderStream(ctx) or render(ctx)");

        const rendered = await render(ctx);
        const html =
          typeof rendered === "string" ? rendered
          : rendered && typeof rendered.html === "string" ? rendered.html
          : "";
        if (!html) throw new Error("SSR render returned empty HTML");
        process.stdout.write(JSON.stringify({ id, chunk: html }) + "\n");
        process.stdout.write(JSON.stringify({ id, done: true }) + "\n");
      }
    } catch (err) {
      if (id) {
        process.stdout.write(JSON.stringify({ id, error: stringifyError(err) }) + "\n");
      } else {
        console.error(stringifyError(err));
      }
    }
  }
} catch (err) {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
}
`
