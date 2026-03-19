package router

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------
var paramNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Route struct {
	// FilePath is the absolute path to the page file (e.g. /app/pages/blog/[id].tsx).
	FilePath string
	// Pattern is the net/http ServeMux pattern (e.g. /blog/{id}).
	Pattern string
	// BundleKey is the stable identifier used to key the compiled bundle (e.g. blog/[id]).
	BundleKey string
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------
func Scan(pagesDir string) ([]Route, error) {
	var routes []Route

	err := filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		ext := filepath.Ext(path)
		switch ext {
		case ".tsx", ".ts", ".jsx", ".js", ".svelte":
		default:
			return nil
		}

		rel, err := filepath.Rel(pagesDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		base := filepath.Base(path)
		for _, lext := range []string{".tsx", ".ts", ".jsx", ".js"} {
			if base == "_layout"+lext {
				return nil
			}
		}
		if strings.HasPrefix(rel, "api/") {
			return nil
		}

		bundleKey := strings.TrimSuffix(rel, ext)
		pattern, err := fileToPattern(rel)
		if err != nil {
			return fmt.Errorf("invalid route %q: %w", rel, err)
		}
		if strings.HasPrefix(pattern, "/_echo") {
			return fmt.Errorf("invalid route %q: reserved path prefix /_echo", rel)
		}

		routes = append(routes, Route{
			FilePath:  filepath.ToSlash(path),
			Pattern:   pattern,
			BundleKey: bundleKey,
		})
		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern == routes[j].Pattern {
			return routes[i].FilePath < routes[j].FilePath
		}
		return routes[i].Pattern < routes[j].Pattern
	})

	seen := make(map[string]string, len(routes))
	for _, route := range routes {
		if prev, ok := seen[route.Pattern]; ok {
			return nil, fmt.Errorf("conflicting routes for pattern %q: %s and %s", route.Pattern, prev, route.FilePath)
		}
		seen[route.Pattern] = route.FilePath
	}

	return routes, nil
}

func ScanAPI(pagesDir string) ([]Route, error) {
	apiDir := filepath.Join(pagesDir, "api")
	if _, err := os.Stat(apiDir); err != nil {
		return nil, nil
	}
	routes, err := Scan(apiDir)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		routes[i].BundleKey = "api/" + routes[i].BundleKey
		if routes[i].Pattern == "/" {
			routes[i].Pattern = "/api"
		} else {
			routes[i].Pattern = "/api" + routes[i].Pattern
		}
	}
	return routes, nil
}

func fileToPattern(rel string) (string, error) {
	noExt := strings.TrimSuffix(rel, filepath.Ext(rel))
	parts := strings.Split(noExt, "/")

	var segments []string
	for _, p := range parts {
		switch {
		case p == "index":
		case strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]"):
			wildcard, err := wildcardForSegment(p)
			if err != nil {
				return "", err
			}
			segments = append(segments, wildcard)
		default:
			segments = append(segments, p)
		}
	}

	if len(segments) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(segments, "/"), nil
}

func wildcardForSegment(segment string) (string, error) {
	inner := segment[1 : len(segment)-1]
	if inner == "" {
		return "", fmt.Errorf("empty dynamic segment %q", segment)
	}

	catchAll := false
	if strings.HasPrefix(inner, "...") {
		catchAll = true
		inner = strings.TrimPrefix(inner, "...")
	}

	if inner == "" {
		return "", fmt.Errorf("missing parameter name in %q", segment)
	}
	if !paramNameRE.MatchString(inner) {
		return "", fmt.Errorf("invalid parameter name %q", inner)
	}

	if catchAll {
		return "{" + inner + "...}", nil
	}
	return "{" + inner + "}", nil
}
