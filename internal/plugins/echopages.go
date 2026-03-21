package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

const virtualEchoPages = "virtual:echo-pages"
const echoPagesNamespace = "echo-pages"

// EchoPages returns an esbuild plugin that resolves "virtual:echo-pages" by
// scanning the app's pages/ directory. The generated module has the same
// contract as the Vite echo-pages plugin, enabling client.tsx to set up
// React Router with all routes and their layout chains.
func EchoPages(appDir string) esbuild.Plugin {
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
					contents, err := generateEchoPagesModule(appDir)
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

var echoPagesExts = []string{".tsx", ".jsx", ".ts", ".js"}

func generateEchoPagesModule(appDir string) (string, error) {
	pagesDir := filepath.Join(appDir, "pages")
	entries, err := scanEchoPages(pagesDir, appDir)
	if err != nil {
		return "", fmt.Errorf("echo-pages plugin: %w", err)
	}

	var sb strings.Builder

	sb.WriteString(`export function echoPatternToPath(pattern) {
  return pattern
    .replace(/\{([^}]+)\.\.\.\}/g, "*")
    .replace(/\{([^}]+)\}/g, ":$1");
}
`)

	sb.WriteString("export const pages = {\n")
	for _, e := range entries {
		absFile := filepath.ToSlash(filepath.Join(appDir, e.relFile))
		sb.WriteString(fmt.Sprintf("  %q: {\n", e.pattern))
		sb.WriteString(fmt.Sprintf("    load: () => import(%q),\n", absFile))
		sb.WriteString("    layouts: [\n")
		for _, lf := range e.layouts {
			absLayout := filepath.ToSlash(filepath.Join(appDir, lf))
			sb.WriteString(fmt.Sprintf("      () => import(%q),\n", absLayout))
		}
		sb.WriteString("    ],\n")
		sb.WriteString("  },\n")
	}
	sb.WriteString("};\n")
	return sb.String(), nil
}

type echoPageEntry struct {
	pattern string
	relFile string
	layouts []string
}

func scanEchoPages(pagesDir, appDir string) ([]echoPageEntry, error) {
	var results []echoPageEntry
	if err := walkEchoPages(pagesDir, appDir, "", &results); err != nil {
		return nil, err
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].pattern < results[j].pattern
	})
	return results, nil
}

func walkEchoPages(dir, appDir, base string, out *[]echoPageEntry) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, de := range entries {
		name := de.Name()
		rel := name
		if base != "" {
			rel = base + "/" + name
		}
		if de.IsDir() {
			if name == "api" {
				continue
			}
			if err := walkEchoPages(filepath.Join(dir, name), appDir, rel, out); err != nil {
				return err
			}
			continue
		}
		if !echoPagesHasExt(name) || isEchoPageSkipped(name) {
			continue
		}
		pattern := echoPagesFileToPattern(rel)
		layouts := echoPagesFindLayouts(filepath.Join(appDir, "pages"), rel)
		relLayouts := make([]string, len(layouts))
		for i, l := range layouts {
			relLayouts[i] = filepath.ToSlash(filepath.Join("pages", l))
		}
		*out = append(*out, echoPageEntry{
			pattern: pattern,
			relFile: filepath.ToSlash(filepath.Join("pages", rel)),
			layouts: relLayouts,
		})
	}
	return nil
}

func echoPagesHasExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	for _, e := range echoPagesExts {
		if ext == e {
			return true
		}
	}
	return false
}

func isEchoPageSkipped(name string) bool {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	if strings.HasPrefix(base, "_") {
		return true
	}
	if strings.Contains(name, ".loader.") || strings.Contains(name, ".meta.") || strings.Contains(name, ".d.") {
		return true
	}
	return base == "404" || base == "500"
}

func echoPagesFileToPattern(rel string) string {
	rel = filepath.ToSlash(rel)
	for _, ext := range echoPagesExts {
		if strings.HasSuffix(rel, ext) {
			rel = rel[:len(rel)-len(ext)]
			break
		}
	}
	segs := strings.Split(rel, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch {
		case strings.HasPrefix(seg, "[...") && strings.HasSuffix(seg, "]"):
			out = append(out, "{"+seg[4:len(seg)-1]+"...}")
		case strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]"):
			out = append(out, "{"+seg[1:len(seg)-1]+"}")
		default:
			out = append(out, seg)
		}
	}
	rel = strings.Join(out, "/")
	if rel == "index" {
		return "/"
	}
	if strings.HasSuffix(rel, "/index") {
		rel = rel[:len(rel)-6]
	}
	return "/" + rel
}

func echoPagesFindLayouts(pagesDir, relPage string) []string {
	relPage = filepath.ToSlash(relPage)
	dir := filepath.ToSlash(filepath.Dir(relPage))
	var parts []string
	if dir != "." {
		parts = strings.Split(dir, "/")
	}

	var chain []string
	for depth := 0; depth <= len(parts); depth++ {
		var checkDir string
		if depth == 0 {
			checkDir = pagesDir
		} else {
			checkDir = filepath.Join(pagesDir, filepath.Join(parts[:depth]...))
		}
		for _, ext := range echoPagesExts {
			candidate := filepath.Join(checkDir, "_layout"+ext)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				rel, _ := filepath.Rel(pagesDir, candidate)
				chain = append(chain, filepath.ToSlash(rel))
				break
			}
		}
	}
	return chain
}
