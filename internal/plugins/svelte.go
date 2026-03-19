package plugins

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/echo-ssr/echo/internal/jsruntime"
)

// ---------------------------------------------------------------------------
// Svelte
// ---------------------------------------------------------------------------
func FindSvelte(appDir string) bool {
	info, err := os.Stat(filepath.Join(appDir, "node_modules", "svelte"))
	return err == nil && info.IsDir()
}

func SveltePlugin(appDir string) api.Plugin {
	return api.Plugin{
		Name: "svelte",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: `\.svelte$`},
				func(args api.OnLoadArgs) (api.OnLoadResult, error) {
					script := `
const { compile } = require('svelte/compiler');
const fs = require('fs');
const { js } = compile(fs.readFileSync(process.argv[1], 'utf8'), {
    filename: process.argv[1],
    generate: 'dom',
    format:   'esm',
    runes:    true,
});
process.stdout.write(js.code);
`
					rt, err := jsruntime.Require()
				if err != nil {
					return api.OnLoadResult{}, err
				}
				cmd := exec.Command(rt, "-e", script, args.Path)
					cmd.Dir = appDir
					out, err := cmd.CombinedOutput()
					if err != nil {
						return api.OnLoadResult{},
							fmt.Errorf("svelte compile %s: %w\n%s", args.Path, err, out)
					}
					s := string(out)
					return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
				})
		},
	}
}
