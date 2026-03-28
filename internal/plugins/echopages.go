package plugins

import (
	"fmt"
	"path/filepath"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/config"
	"github.com/echo-ssr/echo/internal/layout"
	"github.com/echo-ssr/echo/internal/router"
)

const virtualEchoPages = "virtual:echo-pages"
const echoPagesNamespace = "echo-pages"

// EchoPages returns an esbuild plugin that resolves "virtual:echo-pages" by
// scanning the configured pages directory. The generated module has the same
// contract as the Vite echo-pages plugin, enabling client.tsx to set up
// React Router with all routes and their layout chains.
func EchoPages(appDir, pagesDir, basePath string) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "echo-pages",
		Setup: func(build esbuild.PluginBuild) {
			build.OnResolve(esbuild.OnResolveOptions{Filter: `^virtual:echo-pages$`},
				func(_ esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
					return esbuild.OnResolveResult{
						Path:      "echo-pages",
						Namespace: echoPagesNamespace,
					}, nil
				})

			build.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: echoPagesNamespace},
				func(_ esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
					contents, err := generateEchoPagesModule(appDir, pagesDir, basePath)
					if err != nil {
						return esbuild.OnLoadResult{}, err
					}
					return esbuild.OnLoadResult{
						Contents: &contents,
						Loader:   esbuild.LoaderJS,
					}, nil
				})
		},
	}
}

func generateEchoPagesModule(appDir, pagesDir, basePath string) (string, error) {
	routes, err := router.Scan(pagesDir)
	if err != nil {
		return "", fmt.Errorf("echo-pages plugin: %w", err)
	}
	layoutMap, err := layout.Find(pagesDir)
	if err != nil {
		return "", fmt.Errorf("echo-pages plugin layouts: %w", err)
	}

	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("export const echoBasePath = %q;\n", config.NormalizeBasePath(basePath)))

	sb.WriteString(`export function echoWithBasePath(path) {
  if (!path) path = "/";
  if (!path.startsWith("/")) path = "/" + path;
  if (!echoBasePath) return path;
  return path === "/" ? echoBasePath : echoBasePath + path;
}

export function echoDataPath(pathname, search = "") {
  const dataPath = pathname === "/" ? "/_echo/data/" : "/_echo/data" + pathname;
  return echoWithBasePath(dataPath) + (search || "");
}

export function echoPatternToPath(pattern) {
  return pattern
    .replace(/\{([^}]+)\.\.\.\}/g, "*")
    .replace(/\{([^}]+)\}/g, ":$1");
}
`)

	sb.WriteString("export const pages = {\n")
	for _, route := range routes {
		if router.IsErrorPageFile(route.FilePath) {
			continue
		}

		relFile, err := filepath.Rel(appDir, filepath.FromSlash(route.FilePath))
		if err != nil {
			return "", fmt.Errorf("relative page path for %s: %w", route.FilePath, err)
		}

		sb.WriteString(fmt.Sprintf("  %q: {\n", route.Pattern))
		absFile := filepath.ToSlash(filepath.Join(appDir, filepath.ToSlash(relFile)))
		sb.WriteString(fmt.Sprintf("    load: () => import(%q),\n", absFile))
		sb.WriteString("    layouts: [\n")
		for _, layoutFile := range layoutMap[route.BundleKey] {
			relLayout, err := filepath.Rel(appDir, filepath.FromSlash(layoutFile))
			if err != nil {
				return "", fmt.Errorf("relative layout path for %s: %w", layoutFile, err)
			}
			absLayout := filepath.ToSlash(filepath.Join(appDir, filepath.ToSlash(relLayout)))
			sb.WriteString(fmt.Sprintf("      () => import(%q),\n", absLayout))
		}
		sb.WriteString("    ],\n")
		sb.WriteString("  },\n")
	}
	sb.WriteString("};\n")
	return sb.String(), nil
}
