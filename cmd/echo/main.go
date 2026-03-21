package main

import (
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/echo-ssr/echo/internal/config"
	"github.com/echo-ssr/echo/internal/jsruntime"
	"github.com/echo-ssr/echo/internal/server"
)

const usage = `Echo - fast, minimal SSR framework

Usage:
  echo init    [app-dir] [--node]    Scaffold a new React+Vite app  (default: my-echo-app)
  echo dev     [app-dir]             Start the dev server      (default: ., requires Node.js)
  echo build   [app-dir] [--static]  Compile bundles to dist/  (--static also writes HTML, requires Node.js)
  echo start   [app-dir]             Start the production server (requires Node.js)
  echo version                       Print the Echo version

Flags for init:
  --node     Add JS loader support (requires Node.js at runtime)
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "init":
		dir, node := parseInitArgs(os.Args[2:])
		runInit(dir, node)
		return
	case "version":
		fmt.Println("echo version " + server.Version)
		return
	}

	appDir := "."
	if len(os.Args) > 2 {
		appDir = os.Args[2]
	}

	staticFlag := false
	for _, a := range os.Args[3:] {
		if a == "--static" {
			staticFlag = true
		}
	}

	switch cmd {
	case "dev":
		runServer(appDir, true)
	case "build":
		if staticFlag {
			runBuildStatic(appDir)
		} else {
			runBuild(appDir)
		}
	case "start":
		runServer(appDir, false)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(1)
	}
}

func parseInitArgs(args []string) (dir string, node bool) {
	dir = "my-echo-app"
	for _, a := range args {
		switch {
		case a == "--node":
			node = true
		case !strings.HasPrefix(a, "--"):
			dir = a
		}
	}
	return
}

func runServer(appDir string, devMode bool) {
	requireNode()

	var (
		srv *server.Server
		err error
	)
	if devMode {
		srv, err = server.New(appDir, true)
	} else {
		srv, err = server.NewProduction(appDir)
	}
	if err != nil {
		log.Fatalf("[echo] failed to start: %v", err)
	}

	portStr := os.Getenv("PORT")
	if portStr == "" {
		cfg, _ := config.Load(appDir)
		portStr = strconv.Itoa(cfg.Port)
	} else if _, err := strconv.Atoi(portStr); err != nil {
		log.Fatalf("[echo] invalid PORT %q: must be a number", portStr)
	}

	if err := srv.Start(":" + portStr); err != nil {
		log.Fatalf("[echo] server error: %v", err)
	}
}

func runBuild(appDir string) {
	requireNode()

	if err := server.Build(appDir); err != nil {
		log.Fatalf("[echo] build failed: %v", err)
	}
	log.Printf("[echo] build complete -> dist/")
}

func runBuildStatic(appDir string) {
	requireNode()

	if err := server.BuildStatic(appDir); err != nil {
		log.Fatalf("[echo] static build failed: %v", err)
	}
	log.Printf("[echo] static build complete -> dist/")
}

func requireNode() {
	if _, err := jsruntime.Require(); err != nil {
		log.Fatalf("[echo] %v", err)
	}
}

// ---------------------------------------------------------------------------
// echo init templates
// ---------------------------------------------------------------------------

var reactTemplate = map[string]string{
	"client.tsx": `import { hydrateRoot, createRoot } from "react-dom/client";
import { createElement } from "react";
import type { ComponentType, FC, ReactNode } from "react";

// pages is auto-generated from your pages/ directory by Echo's Vite plugin.
// Each entry exposes the page component and its layout chain so you can wire
// up any client-side router you like (React Router, TanStack Router, wouter…).
//
// echoPatternToPath converts Echo patterns to :param-style router paths:
//   /blog/{id}       → /blog/:id
//   /files/{slug...} → /files/*
//
// /_echo/data/<path> is a JSON endpoint for each route's loader data —
// call it from your router's loader function on client-side navigation.
//
// Example with React Router v7:
//
//   import { createBrowserRouter, RouterProvider, useLoaderData } from "react-router-dom";
//   import { pages, echoPatternToPath } from "virtual:echo-pages";
//
//   const router = createBrowserRouter(
//     Object.entries(pages).map(([pattern, { load, layouts }], idx) => ({
//       id: String(idx),
//       path: echoPatternToPath(pattern),
//       lazy: async () => {
//         const [page, ...ls] = await Promise.all([load(), ...layouts.map(l => l())]);
//         const Page = page.default;
//         function Route() {
//           const data = useLoaderData();
//           let node = createElement(Page, { loaderData: data });
//           for (let i = ls.length - 1; i >= 0; i--)
//             if (ls[i].default) node = createElement(ls[i].default, null, node);
//           return node;
//         }
//         return { Component: Route };
//       },
//       loader: async ({ request }) => {
//         const url = new URL(request.url);
//         const res = await fetch("/_echo/data" + url.pathname + url.search);
//         return res.ok ? res.json() : null;
//       },
//     }))
//   );
//
//   export function mount(root: Element) {
//     hydrateRoot(root, createElement(RouterProvider, { router }));
//   }

type PageModule = { default?: ComponentType<any> };
type LayoutModule = { default?: FC<{ children: ReactNode }> };

export function useLoaderData<T = unknown>(): T | null {
  const el = document.getElementById("__echo_data__");
  return el ? (JSON.parse(el.textContent ?? "null") as T) : null;
}

export function mount(root: Element, layouts: LayoutModule[], pageModule: PageModule) {
  if (!pageModule.default) throw new Error("Page module missing a default export");
  const loaderData = useLoaderData();
  let content = createElement(pageModule.default, { loaderData });
  for (let i = layouts.length - 1; i >= 0; i--) {
    if (layouts[i].default) content = createElement(layouts[i].default!, null, content);
  }
  if (root.innerHTML.trim() !== "") {
    hydrateRoot(root, content);
  } else {
    createRoot(root).render(content);
  }
}
`,
	"pages/index.tsx": `export default function Home() {
  return (
    <main>
      <h1>Hello from Echo</h1>
      <p>Edit <code>pages/index.tsx</code> and save to hot reload.</p>
    </main>
  );
}
`,
	"pages/_layout.tsx": `import type { ReactNode } from "react";

export default function Layout({ children }: { children: ReactNode }) {
  return <>{children}</>;
}
`,
	"pages/index.meta.json": `{
  "title": "Home",
  "description": "Welcome to my Echo app."
}
`,
	"pages/500.tsx": `interface ErrorData {
  message: string;
  path: string;
  status: number;
}

interface Props {
  loaderData?: ErrorData;
}

export default function ErrorPage({ loaderData }: Props) {
  return (
    <main>
      <h1>Something went wrong</h1>
      {loaderData && <p>{loaderData.message}</p>}
    </main>
  );
}
`,
	"pages/500.meta.json": `{
  "title": "Error",
  "description": "An unexpected error occurred."
}
`,
	"public/.gitkeep": ``,
	"index.html": `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Echo App</title>
  </head>
  <body>
    <div id="vite-root"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
`,
	"src/main.ts": `console.debug("[echo] vite client entry loaded");
`,
	"src/entry-server.tsx": `import { renderToString, renderToPipeableStream } from "react-dom/server";
import { createElement } from "react";
import { PassThrough, Transform } from "node:stream";
import type { Readable } from "node:stream";
import { pages } from "virtual:echo-pages";

interface RenderContext {
  url: string;
  routePattern: string;
  status: number;
  shell: string;
  loaderData: unknown;
}

function serializeLoaderData(loaderData: unknown): string {
  return JSON.stringify(loaderData ?? null).replace(/</g, "\\u003c");
}

function buildShellParts(shell: string, loaderData: unknown) {
  const rootMarker = '<div id="root"></div>';
  const markerIdx = shell.indexOf(rootMarker);
  if (markerIdx < 0) return null;

  const shellHead = shell.slice(0, markerIdx) + '<div id="root">';
  const afterRoot = shell.slice(markerIdx + rootMarker.length);
  const dataScript =
    '<script id="__echo_data__" type="application/json">' +
    serializeLoaderData(loaderData) +
    "</script>";
  const bodyClose = afterRoot.lastIndexOf("</body>");
  const shellTail =
    "</div>" +
    (bodyClose >= 0
      ? afterRoot.slice(0, bodyClose) + dataScript + afterRoot.slice(bodyClose)
      : afterRoot + dataScript);

  return { shellHead, shellTail };
}

// buildApp loads the page and its layout chain, returning a React element tree
// with layouts wrapping the page (outermost layout first).
async function buildApp(routePattern: string, loaderData: unknown) {
  const entry = pages[routePattern];
  if (!entry) return null;
  const [pageMod, ...layoutMods] = await Promise.all([
    entry.load(),
    ...entry.layouts.map((l) => l()),
  ]);
  let node = createElement(pageMod.default, { loaderData });
  for (let i = layoutMods.length - 1; i >= 0; i--) {
    if (layoutMods[i].default) node = createElement(layoutMods[i].default!, null, node);
  }
  return node;
}

// renderStream uses renderToPipeableStream for true streaming SSR.
// React emits the shell immediately then fills Suspense boundaries as async
// data resolves. The Go server pipes each chunk straight to the HTTP response
// so the browser starts rendering before the full page is ready.
export async function renderStream(ctx: RenderContext): Promise<Readable> {
  const parts = buildShellParts(ctx.shell, ctx.loaderData);
  const app = await buildApp(ctx.routePattern, ctx.loaderData);

  if (!app || !parts) {
    const p = new PassThrough();
    p.end(ctx.shell);
    return p;
  }

  const { shellHead, shellTail } = parts;
  const output = new PassThrough();

  const { pipe } = renderToPipeableStream(app, {
    onShellReady() {
      output.write(shellHead);
      // Transform appends shellTail in flush(), which runs after React calls
      // wrap.end() — i.e. after all content including deferred Suspense fills.
      const wrap = new Transform({
        transform(chunk, _enc, cb) { this.push(chunk); cb(); },
        flush(cb) { this.push(shellTail); cb(); },
      });
      wrap.pipe(output, { end: true });
      pipe(wrap);
    },
    onShellError(err) {
      output.destroy(err instanceof Error ? err : new Error(String(err)));
    },
    onError(err) {
      console.error("[echo] SSR render error:", err);
    },
  });

  return output;
}

// render is the non-streaming fallback used by static builds and error pages.
export async function render(ctx: RenderContext): Promise<string> {
  const app = await buildApp(ctx.routePattern, ctx.loaderData);
  const appHtml = app ? renderToString(app) : "";

  let html = ctx.shell.replace(
    '<div id="root"></div>',
    '<div id="root">' + appHtml + "</div>"
  );
  const dataScript =
    '<script id="__echo_data__" type="application/json">' +
    serializeLoaderData(ctx.loaderData) +
    "</script>";
  const closeBody = html.lastIndexOf("</body>");
  if (closeBody >= 0) {
    html = html.slice(0, closeBody) + dataScript + html.slice(closeBody);
  }
  return html;
}
`,
	"src/echo-env.d.ts": `/// <reference types="vite/client" />

declare module "virtual:echo-pages" {
  import type { ComponentType, FC, ReactNode } from "react";
  type PageLoad = () => Promise<{ default: ComponentType<any> }>;
  type LayoutLoad = () => Promise<{ default: FC<{ children: ReactNode }> }>;

  /** All page routes keyed by Echo pattern (e.g. "/blog/{id}"). */
  export const pages: Record<string, { load: PageLoad; layouts: LayoutLoad[] }>;

  /**
   * Convert an Echo route pattern to a :param-style path for most routers.
   *   /blog/{id}       → /blog/:id
   *   /files/{slug...} → /files/*
   */
  export function echoPatternToPath(pattern: string): string;
}
`,
	"plugins/echo-pages.ts": `import type { Plugin } from "vite";
import fs from "node:fs";
import path from "node:path";

const VIRTUAL_ID = "virtual:echo-pages";
const RESOLVED_ID = "\0" + VIRTUAL_ID;

const PAGE_EXTS = /\.(tsx|jsx|ts|js)$/;
const LAYOUT_EXTS = [".tsx", ".jsx", ".ts", ".js"];
// Skip _layout, .loader, .meta, .d files and server-side error pages
const SKIP_PATTERN = /^_|\.loader\.|\.meta\.|\.d\./;
const ERROR_PAGES = /^(404|500)\./;

function segmentToPattern(seg: string): string {
  if (seg.startsWith("[...") && seg.endsWith("]")) return "{" + seg.slice(4, -1) + "...}";
  if (seg.startsWith("[") && seg.endsWith("]")) return "{" + seg.slice(1, -1) + "}";
  return seg;
}

function fileToRoute(rel: string): string {
  const noExt = rel.replace(PAGE_EXTS, "");
  const segments = noExt.split("/").map(segmentToPattern);
  if (segments[segments.length - 1] === "index") segments.pop();
  const joined = segments.join("/");
  return joined === "" ? "/" : "/" + joined;
}

// Returns _layout.* import paths (relative to pagesDir root) for a page file,
// ordered root-first so the outermost layout wraps first.
function findLayouts(pagesDir: string, relPage: string): string[] {
  const dir = path.dirname(relPage);
  const parts = dir === "." ? [] : dir.split("/");
  const chain: string[] = [];
  for (let depth = 0; depth <= parts.length; depth++) {
    const checkDir =
      depth === 0 ? pagesDir : path.join(pagesDir, ...parts.slice(0, depth));
    for (const ext of LAYOUT_EXTS) {
      const candidate = path.join(checkDir, "_layout" + ext);
      if (fs.existsSync(candidate)) {
        chain.push("/" + path.relative(pagesDir, candidate).split(path.sep).join("/"));
        break;
      }
    }
  }
  return chain;
}

function scanDir(pagesDir: string, dir: string, base = ""): Array<[string, string, string[]]> {
  const results: Array<[string, string, string[]]> = [];
  let entries: string[];
  try {
    entries = fs.readdirSync(dir);
  } catch {
    return results;
  }
  for (const entry of entries) {
    const full = path.join(dir, entry);
    const rel = base ? base + "/" + entry : entry;
    const stat = fs.statSync(full, { throwIfNoEntry: false });
    if (!stat) continue;
    if (stat.isDirectory()) {
      if (entry === "api") continue;
      results.push(...scanDir(pagesDir, full, rel));
    } else if (
      PAGE_EXTS.test(entry) &&
      !SKIP_PATTERN.test(entry) &&
      !ERROR_PAGES.test(entry)
    ) {
      results.push([fileToRoute(rel), "/" + rel, findLayouts(pagesDir, rel)]);
    }
  }
  return results;
}

export function echoPages(): Plugin {
  let pagesDir = "";

  return {
    name: "echo-pages",

    configResolved(config) {
      pagesDir = path.join(config.root, "pages");
    },

    resolveId(id) {
      if (id === VIRTUAL_ID) return RESOLVED_ID;
    },

    load(id) {
      if (id !== RESOLVED_ID) return;
      const entries = scanDir(pagesDir, pagesDir);
      const lines = entries.map(([pattern, file, layouts]) => {
        const layoutImports = layouts
          .map((l) => "      () => import(" + JSON.stringify(l) + "),")
          .join("\n");
        return (
          "  " + JSON.stringify(pattern) + ": {\n" +
          "    load: () => import(" + JSON.stringify(file) + "),\n" +
          "    layouts: [\n" + layoutImports + (layoutImports ? "\n" : "") + "    ],\n" +
          "  },"
        );
      });
      const patternUtil = [
        "// Convert an Echo route pattern to a :param-style router path.",
        "//   /blog/{id}       → /blog/:id",
        "//   /files/{slug...} → /files/*",
        "export function echoPatternToPath(pattern) {",
        "  return pattern",
        '    .replace(/\\{([^}]+)\\.\\.\\.\\}/g, "*")',
        '    .replace(/\\{([^}]+)\\}/g, ":$1");',
        "}",
      ].join("\n");
      return [patternUtil, "export const pages = {", ...lines, "};"].join("\n");
    },

    configureServer(server) {
      const invalidate = () => {
        const mod = server.moduleGraph.getModuleById(RESOLVED_ID);
        if (mod) server.moduleGraph.invalidateModule(mod);
        server.ws.send({ type: "full-reload" });
      };
      server.watcher.on("add", (f) => { if (f.startsWith(pagesDir)) invalidate(); });
      server.watcher.on("unlink", (f) => { if (f.startsWith(pagesDir)) invalidate(); });
    },
  };
}
`,
	"vite.config.ts": `import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { echoPages } from "./plugins/echo-pages";

export default defineConfig({
  plugins: [react(), echoPages()],
});
`,
	"package.json": `{
  "name": "my-echo-app",
  "private": true,
  "scripts": {
    "dev": "echo dev .",
    "build": "echo build .",
    "start": "echo start ."
  },
  "dependencies": {
    "react": "^19.0.0",
    "react-dom": "^19.0.0"
  },
  "devDependencies": {
    "vite": "^6.3.0",
    "@vitejs/plugin-react": "^4.4.0",
    "@types/react": "^19.0.0",
    "@types/react-dom": "^19.0.0",
    "@types/node": "^22.0.0"
  }
}
`,
	"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "skipLibCheck": true
  }
}
`,
	".gitignore": `node_modules/
dist/
`,
}

var reactNodeExtras = map[string]string{
	"pages/index.tsx": `interface Props {
  loaderData?: { message: string };
}

export default function Home({ loaderData }: Props) {
  return (
    <main>
      <h1>{loaderData?.message ?? "Hello from Echo"}</h1>
      <p>Edit <code>pages/index.tsx</code> and save to hot reload.</p>
    </main>
  );
}
`,
	"pages/index.loader.ts": `export async function loader() {
  return { message: "Hello from the server!" };
}
`,
}

func runInit(dir string, node bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("[echo] init: %v", err)
	}

	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
		log.Fatalf("[echo] init: directory %q already exists and is not empty", dir)
	}

	files := make(map[string]string, len(reactTemplate)+len(reactNodeExtras))
	maps.Copy(files, reactTemplate)
	if node {
		maps.Copy(files, reactNodeExtras)
	}

	appName := filepath.Base(abs)
	for rel, content := range files {
		content = strings.ReplaceAll(content, "my-echo-app", appName)
		dst := filepath.Join(abs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			log.Fatalf("[echo] init: %v", err)
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			log.Fatalf("[echo] init: %v", err)
		}
	}

	variant := "react"
	if node {
		variant += " + node"
	}
	fmt.Printf("Echo app created in %s/ (%s)\n\nNext steps:\n  cd %s\n  npm install\n  echo dev .\n",
		dir, variant, dir)
}
