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
	"github.com/echo-ssr/echo/internal/server"
)

const usage = `Echo - fast, minimal SSR framework

Usage:
  echo init  [app-dir] [--svelte] [--vue] [--node]   Scaffold a new Echo app  (default: my-echo-app, react)
  echo dev   [app-dir]                                Start the dev server      (default: .)
  echo build [app-dir] [--static]                     Compile bundles to dist/  (--static also writes HTML)
  echo start [app-dir]                                Start the production server

Flags for init:
  --svelte   Use Svelte instead of React
  --vue      Use Vue instead of React
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
		dir, template, node := parseInitArgs(os.Args[2:])
		runInit(dir, template, node)
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

// parseInitArgs extracts the optional directory and template from the args
// that follow "echo init". Recognised flags: --svelte, --vue, --node. All
// other non-flag args are treated as the target directory.
func parseInitArgs(args []string) (dir, template string, node bool) {
	dir = "my-echo-app"
	template = "react"
	for _, a := range args {
		switch {
		case a == "--svelte":
			template = "svelte"
		case a == "--vue":
			template = "vue"
		case a == "--node":
			node = true
		case !strings.HasPrefix(a, "--"):
			dir = a
		}
	}
	return
}

func runServer(appDir string, devMode bool) {
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

	port := os.Getenv("PORT")
	if port == "" {
		cfg, _ := config.Load(appDir)
		port = cfg.Port
	}
	if _, err := strconv.Atoi(port); err != nil {
		log.Fatalf("[echo] invalid port %q: must be a number", port)
	}

	if err := srv.Start(":" + port); err != nil {
		log.Fatalf("[echo] server error: %v", err)
	}
}

func runBuild(appDir string) {
	if err := server.Build(appDir); err != nil {
		log.Fatalf("[echo] build failed: %v", err)
	}
	log.Printf("[echo] build complete -> dist/")
}

func runBuildStatic(appDir string) {
	if err := server.BuildStatic(appDir); err != nil {
		log.Fatalf("[echo] static build failed: %v", err)
	}
	log.Printf("[echo] static build complete -> dist/")
}

// ---------------------------------------------------------------------------
// echo init templates
// ---------------------------------------------------------------------------

var reactTemplate = map[string]string{
	"client.tsx": `import { createRoot } from "react-dom/client";
import { createElement } from "react";
import type { ComponentType, FC, ReactNode } from "react";

type PageModule = { default?: ComponentType<any> };
type LayoutModule = { default?: FC<{ children: ReactNode }> };

export function useLoaderData<T = unknown>(): T | null {
  const el = document.getElementById("__echo_data__");
  return el ? (JSON.parse(el.textContent ?? "null") as T) : null;
}

export function mount(root: Element, layouts: LayoutModule[], pageModule: PageModule) {
  if (!pageModule.default) throw new Error("Page module missing a default export");
  const el = document.getElementById("__echo_data__");
  const loaderData = el ? JSON.parse(el.textContent ?? "null") : null;
  let content = createElement(pageModule.default, { loaderData });
  for (let i = layouts.length - 1; i >= 0; i--) {
    if (layouts[i].default) content = createElement(layouts[i].default!, null, content);
  }
  createRoot(root).render(content);
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
	"package.json": `{
  "name": "my-echo-app",
  "private": true,
  "dependencies": {
    "react": "^18.3.0",
    "react-dom": "^18.3.0"
  },
  "devDependencies": {
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0"
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

// reactNodeExtras are merged into reactTemplate when --node is passed.
// They override pages/index.tsx with a loader-aware version and add
// the loader file. Requires Node.js at runtime.
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

var svelteTemplate = map[string]string{
	"client.ts": `import { mount as svelteMount } from "svelte";

export function useLoaderData<T = unknown>(): T | null {
  const el = document.getElementById("__echo_data__");
  return el ? (JSON.parse(el.textContent ?? "null") as T) : null;
}

export function mount(root: Element, layouts: { default?: any }[], pageModule: { default?: any }) {
  if (!pageModule.default) throw new Error("Page module missing a default export");
  const el = document.getElementById("__echo_data__");
  const loaderData = el ? JSON.parse(el.textContent ?? "null") : null;
  // Svelte layouts wrap via slots; mount the outermost layout if present.
  const entry = layouts.length > 0 && layouts[0].default ? layouts[0].default : pageModule.default;
  svelteMount(entry, { target: root, props: { loaderData } });
}
`,
	"pages/_layout.svelte": `<script>
  let { children } = $props();
</script>

{@render children()}
`,
	"pages/index.svelte": `<script>
  let count = $state(0);
</script>

<main>
  <h1>Hello from Echo</h1>
  <button onclick={() => count++}>
    clicked {count} {count === 1 ? "time" : "times"}
  </button>
</main>
`,
	"pages/index.meta.json": `{
  "title": "Home",
  "description": "Welcome to my Echo app."
}
`,
	"pages/500.svelte": `<script>
  let { loaderData } = $props();
</script>

<main>
  <h1>Something went wrong</h1>
  {#if loaderData}
    <p>{loaderData.message}</p>
  {/if}
</main>
`,
	"pages/500.meta.json": `{
  "title": "Error",
  "description": "An unexpected error occurred."
}
`,
	"public/.gitkeep": ``,
	"package.json": `{
  "name": "my-echo-app",
  "private": true,
  "dependencies": {
    "svelte": "^5.0.0"
  }
}
`,
	"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "skipLibCheck": true
  }
}
`,
	".gitignore": `node_modules/
dist/
`,
}

var svelteNodeExtras = map[string]string{
	"pages/index.svelte": `<script>
  let { loaderData } = $props();
  let count = $state(0);
</script>

<main>
  <h1>{loaderData?.message ?? "Hello from Echo"}</h1>
  <button onclick={() => count++}>
    clicked {count} {count === 1 ? "time" : "times"}
  </button>
</main>
`,
	"pages/index.loader.ts": `export async function loader() {
  return { message: "Hello from the server!" };
}
`,
}

var vueTemplate = map[string]string{
	"client.ts": `import { createApp, h } from "vue";

export function useLoaderData<T = unknown>(): T | null {
  const el = document.getElementById("__echo_data__");
  return el ? (JSON.parse(el.textContent ?? "null") as T) : null;
}

export function mount(root: Element, layouts: { default?: any }[], pageModule: { default?: any }) {
  if (!pageModule.default) throw new Error("Page module missing a default export");
  const el = document.getElementById("__echo_data__");
  const loaderData = el ? JSON.parse(el.textContent ?? "null") : null;
  let vnode = h(pageModule.default, { loaderData });
  for (let i = layouts.length - 1; i >= 0; i--) {
    if (layouts[i].default) {
      const inner = vnode;
      const layout = layouts[i].default;
      vnode = h(layout, null, { default: () => inner });
    }
  }
  createApp({ render: () => vnode }).mount(root);
}
`,
	"pages/index.vue": `<template>
  <main>
    <h1>Hello from Echo</h1>
    <p>Edit <code>pages/index.vue</code> and save to hot reload.</p>
  </main>
</template>
`,
	"pages/_layout.vue": `<template>
  <slot />
</template>
`,
	"pages/index.meta.json": `{
  "title": "Home",
  "description": "Welcome to my Echo app."
}
`,
	"pages/500.vue": `<template>
  <main>
    <h1>Something went wrong</h1>
    <p v-if="loaderData">{{ loaderData.message }}</p>
  </main>
</template>

<script setup>
const props = defineProps({ loaderData: Object });
</script>
`,
	"pages/500.meta.json": `{
  "title": "Error",
  "description": "An unexpected error occurred."
}
`,
	"public/.gitkeep": ``,
	"package.json": `{
  "name": "my-echo-app",
  "private": true,
  "dependencies": {
    "vue": "^3.5.0"
  },
  "devDependencies": {
    "@vue/compiler-sfc": "^3.5.0"
  }
}
`,
	"tsconfig.json": `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "skipLibCheck": true
  }
}
`,
	".gitignore": `node_modules/
dist/
`,
}

var vueNodeExtras = map[string]string{
	"pages/index.vue": `<template>
  <main>
    <h1>{{ loaderData?.message ?? "Hello from Echo" }}</h1>
    <p>Edit <code>pages/index.vue</code> and save to hot reload.</p>
  </main>
</template>

<script setup>
const props = defineProps({ loaderData: Object });
</script>
`,
	"pages/index.loader.ts": `export async function loader() {
  return { message: "Hello from the server!" };
}
`,
}

func runInit(dir, template string, node bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("[echo] init: %v", err)
	}

	if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
		log.Fatalf("[echo] init: directory %q already exists and is not empty", dir)
	}

	base := reactTemplate
	extras := reactNodeExtras
	switch template {
	case "svelte":
		base = svelteTemplate
		extras = svelteNodeExtras
	case "vue":
		base = vueTemplate
		extras = vueNodeExtras
	}

	files := make(map[string]string, len(base)+len(extras))
	maps.Copy(files, base)
	if node {
		maps.Copy(files, extras)
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

	variant := template
	if node {
		variant += " + node"
	}
	fmt.Printf("Echo app created in %s/ (%s)\n\nNext steps:\n  cd %s\n  npm install\n  echo dev .\n",
		dir, variant, dir)
}
