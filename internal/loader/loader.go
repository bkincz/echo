package loader

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

type Loader struct {
	scriptPath string
}

func (l *Loader) Close() {
	os.Remove(l.scriptPath)
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
// Build + Run
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// API runner
// ---------------------------------------------------------------------------
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

type APIRunner struct {
	scriptPath string
}

func (a *APIRunner) Close() {
	os.Remove(a.scriptPath)
}

const apiRunnerScript = `
const mod = require(process.argv[1]);
let raw = '';
process.stdin.on('data', c => raw += c);
process.stdin.on('end', () => {
  const req = JSON.parse(raw);
  const fn = mod.handler || mod.default;
  Promise.resolve(typeof fn === 'function' ? fn(req) : null)
    .then(res => {
      if (!res) { process.stdout.write(JSON.stringify({status:404,headers:{},body:null})); return; }
      const body = (res.body !== undefined && typeof res.body !== 'string')
        ? JSON.stringify(res.body) : (res.body ?? null);
      process.stdout.write(JSON.stringify({
        status: res.status || 200,
        headers: res.headers || {},
        body,
      }));
    })
    .catch(err => { process.stderr.write(err.message || String(err)); process.exit(1); });
});
`

func BuildAPI(appDir, filePath string) (*APIRunner, error) {
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
		os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()
	return &APIRunner{scriptPath: tmp.Name()}, nil
}

func (a *APIRunner) Run(req APIRequest) (APIResponse, error) {
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return APIResponse{}, err
	}
	rt, err := jsruntime.Require()
	if err != nil {
		return APIResponse{}, err
	}
	cmd := exec.Command(rt, "-e", apiRunnerScript, a.scriptPath)
	cmd.Stdin = strings.NewReader(string(reqJSON))
	out, err := cmd.Output()
	if err != nil {
		return APIResponse{}, fmt.Errorf("api handler execution failed: %w", err)
	}
	var resp APIResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return APIResponse{}, fmt.Errorf("decoding api response: %w", err)
	}
	if resp.Status == 0 {
		resp.Status = 200
	}
	return resp, nil
}

// ---------------------------------------------------------------------------
// Loader runner
// ---------------------------------------------------------------------------
const runnerScript = `
const mod = require(process.argv[1]);
let raw = '';
process.stdin.on('data', c => raw += c);
process.stdin.on('end', () => {
  const ctx = JSON.parse(raw);
  const fn = mod.loader;
  Promise.resolve(typeof fn === 'function' ? fn(ctx) : null)
    .then(data => process.stdout.write(JSON.stringify(data ?? null)))
    .catch(err => { process.stderr.write(err.message || String(err)); process.exit(1); });
});
`

func Build(appDir, filePath string) (*Loader, error) {
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
		os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()
	return &Loader{scriptPath: tmp.Name()}, nil
}

const pathsRunnerScript = `
const mod = require(process.argv[1]);
const fn = mod.paths;
Promise.resolve(typeof fn === 'function' ? fn() : [])
  .then(p => process.stdout.write(JSON.stringify(Array.isArray(p) ? p : [])))
  .catch(err => { process.stderr.write(err.message || String(err)); process.exit(1); });
`

func (l *Loader) Paths() ([]map[string]string, error) {
	rt, err := jsruntime.Require()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(rt, "-e", pathsRunnerScript, l.scriptPath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("paths() execution failed: %w", err)
	}
	var paths []map[string]string
	if err := json.Unmarshal(out, &paths); err != nil {
		return nil, fmt.Errorf("decoding paths: %w", err)
	}
	return paths, nil
}

func (l *Loader) Run(ctx Context) (json.RawMessage, error) {
	ctxJSON, err := json.Marshal(ctx)
	if err != nil {
		return nil, err
	}
	rt, err := jsruntime.Require()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(rt, "-e", runnerScript, l.scriptPath)
	cmd.Stdin = strings.NewReader(string(ctxJSON))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("loader execution failed: %w", err)
	}
	return json.RawMessage(out), nil
}
