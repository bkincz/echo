# Echo

A fast, minimal SSR framework for Go. File-based routing, esbuild bundling, dependency-aware hot reload, and a single binary server. Works with React, Svelte, and Vue out of the box.

```bash
go install github.com/echo-ssr/echo/cmd/echo@latest
```

## Getting Started

```bash
echo init my-app        # React (default)
echo init my-app --svelte
echo init my-app --vue

cd my-app
npm install
echo dev .
```

Open `http://localhost:3000`.

Add `--node` to any of the above to scaffold JS loader files that run server-side data fetching via Node.js or Bun:

```bash
echo init my-app --node
echo init my-app --svelte --node
```

---

## File-based Routing

Routes are derived from the `pages/` directory:

```text
pages/index.tsx          →  /
pages/about.tsx          →  /about
pages/blog/index.tsx     →  /blog
pages/blog/[id].tsx      →  /blog/{id}
pages/docs/[...slug].tsx →  /docs/{slug...}
pages/404.tsx            →  served on unmatched routes (404 status)
pages/500.tsx            →  served on loader errors and panics (500 status)
```

Dynamic segments use `[param]`, catch-all segments use `[...param]`. Both receive their values at runtime via `r.PathValue()` in Go loaders, or as `params` in JS loaders.

---

## Layouts

Place a `_layout` file in any directory to wrap all pages in that directory and below:

```text
pages/
  _layout.tsx          ← wraps every page
  index.tsx
  blog/
    _layout.tsx        ← wraps blog/* only, nested inside root layout
    [id].tsx
```

Layouts receive `children` (React) or use `<slot />` (Svelte) / the default slot (Vue). The layout chain is computed at build time, zero runtime overhead.

```tsx
// pages/_layout.tsx
import type { ReactNode } from "react";

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <div>
      <nav>...</nav>
      {children}
    </div>
  );
}
```

---

## Data Loading

### Go loaders (recommended)

Register a loader function per route before calling `Start()`. The return value is JSON-encoded and available client-side via `useLoaderData()`.

```go
srv.Loader("/blog/{id}", func(r *http.Request) (any, error) {
    id := r.PathValue("id")
    post, err := db.GetPost(id)
    return post, err
})
```

```tsx
// pages/blog/[id].tsx
import { useLoaderData } from "../client";

export default function Post({ loaderData }: { loaderData?: Post }) {
  return <article>{loaderData?.title}</article>;
}
```

### JS loaders (opt-in, requires Node.js or Bun)

Scaffold with `echo init --node`, or create `pages/<route>.loader.ts` manually:

```ts
// pages/index.loader.ts
export async function loader({ params, searchParams, headers }) {
  const res = await fetch("https://api.example.com/data");
  return res.json();
}
```

The loader receives `{ params, searchParams, headers }` and its return value is injected into the page shell identically to a Go loader.

Loader data is embedded in the HTML as:

```html
<script id="__echo_data__" type="application/json">
  { "key": "value" }
</script>
```

---

## API Routes

### Go handlers

```go
srv.Handle("GET /api/users", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    json.NewEncoder(w).Encode(users)
}))
```

Go handlers are registered before page routes, so they always take precedence.

### JS API handlers (opt-in, requires Node.js or Bun)

```ts
// pages/api/users.ts
export async function handler(req) {
  return {
    status: 200,
    headers: { "Content-Type": "application/json" },
    body: { users: [] },
  };
}
```

Files under `pages/api/` are auto-detected and served at `/api/<route>`.

---

## Error Pages

### 404

`pages/404.tsx` is served with status `404` for any request that doesn't match a page route or static file.

### 500

`pages/500.tsx` is served with status `500` when a loader returns an error or a panic is recovered. Error context is injected as loader data:

```tsx
interface ErrorData {
  message: string;
  path: string;
  status: number;
}

export default function ErrorPage({ loaderData }: { loaderData?: ErrorData }) {
  return (
    <main>
      <h1>Something went wrong</h1>
      {loaderData && <p>{loaderData.message}</p>}
    </main>
  );
}
```

Panic recovery is applied as the outermost middleware layer, it catches panics anywhere in the request chain.

---

## Configuration

### echo.config.json

```json
{
  "port": "3000",
  "headers": {
    "X-Frame-Options": "DENY",
    "X-Content-Type-Options": "nosniff"
  }
}
```

### echo.config.ts

TypeScript config is supported. Echo compiles it with esbuild (no Node.js required for this step) then executes the result to extract the exported object:

```ts
// echo.config.ts
interface Config {
  port?: string;
  headers?: Record<string, string>;
}

const config: Config = {
  port: "3000",
  headers: { "X-Frame-Options": "DENY" },
};

export default config;
```

`echo.config.ts` takes precedence over `echo.config.json`. Port can also be overridden at runtime via the `PORT` environment variable.

---

## Static Site Generation

```bash
echo build ./my-app --static
```

Generates a flat `index.html` per route into `dist/`, suitable for any CDN or static host. Loader data is embedded in each file, no server required at runtime.

Dynamic routes require a `paths()` export in their loader file:

```ts
// pages/blog/[id].loader.ts
export async function paths() {
  const posts = await fetchAllPosts();
  return posts.map((p) => ({ id: String(p.id) }));
}

export async function loader({ params }) {
  return fetchPost(params.id);
}
```

Or register a Go `PathsFunc` when using Echo as a library:

```go
srv.Paths("/blog/{id}", func() ([]map[string]string, error) {
    ids, _ := db.AllPostIDs()
    return ids, nil
})
server.BuildStatic(".", server.BuildOptions{GoPaths: srv.GoPaths})
```

---

## CSS

Import CSS directly from any page or shared module:

```tsx
import "./about.css";
```

esbuild extracts CSS into a separate bundle and Echo injects `<link rel="stylesheet">` automatically.

**CSS Modules** are supported via `.module.css` files:

```tsx
import styles from "./hero.module.css";
<div className={styles.hero}>...</div>;
```

**Lightning CSS** (autoprefixing, nesting, modern color functions) is enabled automatically when `lightningcss` is installed:

```bash
npm install --save-dev lightningcss
```

---

## Per-page Metadata

Place a `<page>.meta.json` sidecar next to any page:

```json
{
  "title": "About Us",
  "description": "Learn more about our team."
}
```

Without a sidecar the title is derived from the URL pattern. Editing a `.meta.json` file triggers hot reload.

---

## Hot Reload

In dev mode Echo injects an SSE listener. On file changes:

1. Routes are rescanned
2. Only the bundles whose esbuild dependency graph includes the changed file are recompiled
3. Connected browsers reload

Layout file changes trigger a full rebuild for all affected pages. Build errors display as a browser overlay rather than a silent stale reload.

---

## CLI

```text
echo init  [app-dir] [--svelte] [--vue] [--node]   Scaffold a new app
echo dev   [app-dir]                                Dev server with hot reload
echo build [app-dir] [--static]                     Compile bundles to dist/
echo start [app-dir]                                Serve from dist/
```

`PORT` environment variable overrides the configured port:

```bash
PORT=8080 echo start ./my-app
```

---

## Production Workflow

```bash
echo build ./my-app    # writes dist/
echo start ./my-app    # serves from dist/, no recompilation
```

`dist/` layout:

```text
dist/
  manifest.json          # route table read by echo start
  _echo/bundle/*.js      # minified, content-hashed page bundles
  _echo/bundle/*.css     # extracted CSS bundles
  public/                # copied from your app's public/
```

`echo start` reads the manifest and serves everything from `dist/`. No TypeScript, no esbuild, no Node.js at runtime.

### Docker

```dockerfile
FROM golang:1.26 AS build
RUN go install github.com/echo-ssr/echo/cmd/echo@latest
WORKDIR /app
COPY . .
RUN npm ci && echo build .

FROM debian:bookworm-slim
COPY --from=build /go/bin/echo /usr/local/bin/echo
COPY --from=build /app/dist ./dist
CMD ["echo", "start", "."]
```

---

## Go Library Usage

Echo can be embedded in any Go application, giving full access to Go loaders, API handlers, and custom middleware:

```go
package main

import (
    "net/http"
    "github.com/echo-ssr/echo/internal/server"
)

func main() {
    srv, err := server.New(".", false, server.ServerOptions{
        Middleware: []func(http.Handler) http.Handler{
            corsMiddleware,
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    srv.Loader("/", func(r *http.Request) (any, error) {
        return db.GetHomepageData()
    })

    srv.Handle("GET /api/users", usersHandler)

    srv.Start(":3000")
}
```

---

## App Structure

```text
my-app/
  client.tsx              # Exports mount(root, layouts, pageModule)
  echo.config.json        # Optional: port, response headers
  pages/
    _layout.tsx           # Root layout (optional)
    index.tsx             # Route: /
    about.tsx             # Route: /about
    about.meta.json       # Optional per-page title/description
    404.tsx               # Custom 404 page (optional)
    500.tsx               # Custom error page (optional)
    blog/
      _layout.tsx         # Nested layout for /blog/* (optional)
      [id].tsx            # Route: /blog/{id}
      [id].loader.ts      # JS loader (--node only)
    api/
      users.ts            # JS API route: /api/users (--node only)
  public/                 # Static assets served at /
  package.json
  tsconfig.json
```

---

## Health Endpoint

`GET /_echo/health` is available in both dev and production:

```json
{ "status": "ok", "version": "1.0.0" }
```

## JS Runtime

When JS loaders, JS API routes, or the Svelte/Vue compiler plugin are in use, Echo requires Node.js or Bun. **Bun is preferred automatically** when available (~5 ms startup vs ~80 ms for Node). No configuration required, Echo detects whichever runtime is in `PATH`.

If neither is installed, Echo will surface a clear error only when a JS feature is actually invoked. Pure Go projects with no `.loader.ts` files have no runtime dependency.

---

## License

MIT
