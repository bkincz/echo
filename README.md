# Echo

A minimal SSR framework for Go and React. File-based routing, server-side data loading, Vite-backed SSR, and a single Go binary.

**Requirements:** Go 1.24+, Node.js 18+

```bash
go install github.com/echo-ssr/echo/cmd/echo@latest
```

---

## Quick Start

```bash
echo init my-app
cd my-app
npm install
echo dev .
```

Open `http://localhost:3000`. Add `--node` to include JS loader and API route support:

```bash
echo init my-app --node
```

---

## Features

- File-based routing from a `pages/` directory
- Nested layouts via `_layout.tsx`
- Server-side data loading — Go functions or JS loader files
- JS API routes under `pages/api/`
- Streaming SSR via `renderToPipeableStream` — Suspense boundaries resolve as data arrives
- Hot reload in dev with browser error overlay
- Per-page `title` / `description` via `.meta.json` sidecars
- CSS and CSS Modules out of the box. Including Lightning CSS as optional
- Static site generation (`echo build --static`)
- Custom middleware, headers, and esbuild plugins
- Health endpoint at `/_echo/health`

---

## App Structure

```
my-app/
  client.tsx              # Client mount function (hydration entry)
  src/
    entry-server.tsx      # SSR render function
  pages/
    _layout.tsx           # Root layout (optional)
    index.tsx             # Route: /
    about.tsx             # Route: /about
    about.meta.json       # Optional title + description
    404.tsx               # Custom 404 page
    500.tsx               # Custom error page
    blog/
      _layout.tsx         # Nested layout for /blog/*
      [id].tsx            # Route: /blog/{id}
      [id].loader.ts      # JS data loader (--node)
    api/
      users.ts            # API route: /api/users (--node)
  public/                 # Static assets
  echo.config.json
  vite.config.ts
  package.json
```

---

## Routing

Routes are derived from filenames in `pages/`:

```
pages/index.tsx          →  /
pages/about.tsx          →  /about
pages/blog/[id].tsx      →  /blog/{id}
pages/docs/[...slug].tsx →  /docs/{slug...}
pages/404.tsx            →  unmatched routes (404)
pages/500.tsx            →  loader errors and panics (500)
```

---

## Layouts

Place `_layout.tsx` in any directory to wrap all pages at that level and below:

```tsx
// pages/_layout.tsx
export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <>
      <nav>...</nav>
      {children}
    </>
  );
}
```

Layouts nest automatically — a `pages/blog/_layout.tsx` wraps inside the root layout.

---

## Data Loading

### Go loaders

Register a loader per route before calling `Start()`. The return value is available in the page via `loaderData` and on the client via `useLoaderData()`.

```go
srv.Loader("/blog/{id}", func(r *http.Request) (any, error) {
    return db.GetPost(r.PathValue("id"))
})
```

```tsx
// pages/blog/[id].tsx
export default function Post({ loaderData }: { loaderData?: Post }) {
  return <article>{loaderData?.title}</article>;
}
```

### JS loaders (requires `--node`)

```ts
// pages/blog/[id].loader.ts
export async function loader({ params }) {
  return fetchPost(params.id);
}
```

---

## API Routes

### Go handlers

```go
srv.Handle("GET /api/users", usersHandler)
```

### JS handlers (requires `--node`)

```ts
// pages/api/users.ts
export async function handler(req) {
  return { status: 200, body: { users: [] } };
}
```

---

## Client-Side Routing

Echo ships the infrastructure — you choose the router.

**`virtual:echo-pages`** is a generated module that maps every route to its page and layout chain:

```ts
import { pages, echoPatternToPath } from "virtual:echo-pages";

// pages["/{id}"] = { load: () => import("..."), layouts: [() => import("...")] }
// echoPatternToPath("/blog/{id}") → "/blog/:id"
// echoPatternToPath("/files/{slug...}") → "/files/*"
```

**`GET /_echo/data/<path>`** runs the route's loader and returns JSON — call it from your router's loader function on client-side navigation.

Example wiring with React Router v7 (add `react-router-dom` yourself):

```tsx
// client.tsx
import { createBrowserRouter, RouterProvider, useLoaderData } from "react-router-dom";
import { pages, echoPatternToPath } from "virtual:echo-pages";

const router = createBrowserRouter(
  Object.entries(pages).map(([pattern, { load, layouts }], idx) => ({
    id: String(idx),
    path: echoPatternToPath(pattern),
    lazy: async () => {
      const [page, ...ls] = await Promise.all([load(), ...layouts.map(l => l())]);
      function Route() {
        const data = useLoaderData();
        let node = <page.default loaderData={data} />;
        for (let i = ls.length - 1; i >= 0; i--)
          if (ls[i].default) node = <ls[i].default>{node}</ls[i].default>;
        return node;
      }
      return { Component: Route };
    },
    loader: async ({ request }) => {
      const url = new URL(request.url);
      const res = await fetch("/_echo/data" + url.pathname + url.search);
      return res.ok ? res.json() : null;
    },
  }))
);

export function mount(root: Element) {
  hydrateRoot(root, <RouterProvider router={router} />);
}
```

The scaffolded `client.tsx` ships a simpler `mount(root, layouts, page)` for single-page SSR hydration with a comment showing the above pattern.

---

## CSS

Plain CSS and CSS Modules work out of the box:

```tsx
import "./page.css";
import styles from "./hero.module.css";
```

For autoprefixing, nesting, and modern color functions, install [Lightning CSS](https://lightningcss.dev) — Echo picks it up automatically:

```bash
npm install --save-dev lightningcss
```

For SCSS or other preprocessors, configure them in `vite.config.ts` as you normally would.

---

## Configuration

`echo.config.json` (or `echo.config.ts` for TypeScript):

```json
{
  "port": 3000,
  "headers": {
    "Content-Security-Policy": "default-src 'self'"
  },
  "js": {
    "loaderTimeoutMs": 10000,
    "apiTimeoutMs": 10000,
    "pathsTimeoutMs": 10000
  }
}
```

`PORT` env var overrides the configured port. Echo sets `X-Content-Type-Options`, `X-Frame-Options`, and `Referrer-Policy` by default — use `headers` to override them.

---

## CLI

```
echo init   [app-dir] [--node]    Scaffold a new app
echo dev    [app-dir]             Dev server with hot reload
echo build  [app-dir] [--static]  Compile to dist/
echo start  [app-dir]             Serve from dist/
echo version                      Print version
```

---

## Production

```bash
echo build ./my-app
echo start ./my-app
```

`dist/` contains minified, content-hashed bundles and a manifest read by `echo start`.

### Docker

```dockerfile
FROM golang:1.26 AS build
RUN go install github.com/echo-ssr/echo/cmd/echo@latest
WORKDIR /app
COPY . .
RUN npm ci && echo build .

FROM debian:bookworm-slim
RUN apt-get install -y nodejs
COPY --from=build /go/bin/echo /usr/local/bin/echo
COPY --from=build /app/dist ./dist
CMD ["echo", "start", "."]
```

---

## Static Site Generation

```bash
echo build ./my-app --static
```

Generates a flat `index.html` per route into `dist/`. Dynamic routes require a `paths()` export in their loader:

```ts
// pages/blog/[id].loader.ts
export async function paths() {
  const posts = await fetchAllPosts();
  return posts.map((p) => ({ id: String(p.id) }));
}
```

---

## Go Library

Echo can be embedded in any Go application:

```go
srv, err := server.New(".", false, server.ServerOptions{
    Middleware: []func(http.Handler) http.Handler{corsMiddleware},
})

srv.Loader("/", func(r *http.Request) (any, error) {
    return db.GetHomepageData()
})

srv.Handle("GET /api/users", usersHandler)
srv.Start(":3000")
```

---

## License

MIT
