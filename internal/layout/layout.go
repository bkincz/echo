package layout

import (
	"os"
	"path/filepath"
	"strings"
)

// Find scans pagesDir for _layout.{tsx,ts,jsx,js} files and returns a map
// from bundleKey to an ordered slice of absolute layout file paths, from
// outermost (root) to innermost (closest to the page).
//
// For example, given:
//
//	pages/_layout.tsx
//	pages/blog/_layout.tsx
//
// The bundleKey "blog/[id]" maps to:
//
//	["/abs/pages/_layout.tsx", "/abs/pages/blog/_layout.tsx"]
func Find(pagesDir string) (map[string][]string, error) {
	layouts, err := scanLayouts(pagesDir)
	if err != nil {
		return nil, err
	}
	if len(layouts) == 0 {
		return nil, nil
	}

	pages, err := scanPageKeys(pagesDir)
	if err != nil {
		return nil, err
	}

	result := make(map[string][]string, len(pages))
	for _, key := range pages {
		chain := layoutChain(key, layouts)
		if len(chain) > 0 {
			result[key] = chain
		}
	}
	return result, nil
}

func IsLayoutFile(path string) bool {
	base := filepath.Base(path)
	for _, ext := range []string{".tsx", ".ts", ".jsx", ".js"} {
		if base == "_layout"+ext {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------
var layoutExts = []string{"_layout.tsx", "_layout.ts", "_layout.jsx", "_layout.js"}

func scanLayouts(pagesDir string) (map[string]string, error) {
	found := make(map[string]string)
	err := filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		for _, candidate := range layoutExts {
			if name == candidate {
				rel, _ := filepath.Rel(pagesDir, filepath.Dir(path))
				dir := filepath.ToSlash(rel)
				if dir == "." {
					dir = ""
				}
				found[dir] = filepath.ToSlash(filepath.Clean(path))
				break
			}
		}
		return nil
	})
	return found, err
}

func scanPageKeys(pagesDir string) ([]string, error) {
	pageExts := map[string]bool{".tsx": true, ".ts": true, ".jsx": true, ".js": true}
	var keys []string
	err := filepath.WalkDir(pagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if IsLayoutFile(path) {
			return nil
		}
		ext := filepath.Ext(name)
		if !pageExts[ext] {
			return nil
		}
		rel, _ := filepath.Rel(pagesDir, path)
		rel = filepath.ToSlash(rel)
		key := strings.TrimSuffix(rel, ext)
		keys = append(keys, key)
		return nil
	})
	return keys, err
}

func layoutChain(bundleKey string, layouts map[string]string) []string {
	dir := filepath.ToSlash(filepath.Dir(bundleKey))
	if dir == "." {
		dir = ""
	}

	var dirs []string
	for {
		dirs = append(dirs, dir)
		if dir == "" {
			break
		}
		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent == "." {
			parent = ""
		}
		dir = parent
	}

	var chain []string
	for i := len(dirs) - 1; i >= 0; i-- {
		if lp, ok := layouts[dirs[i]]; ok {
			chain = append(chain, lp)
		}
	}
	return chain
}
