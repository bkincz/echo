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
// Vue
// ---------------------------------------------------------------------------
func FindVue(appDir string) bool {
	vueInfo, err1 := os.Stat(filepath.Join(appDir, "node_modules", "vue"))
	sfcInfo, err2 := os.Stat(filepath.Join(appDir, "node_modules", "@vue", "compiler-sfc"))
	return err1 == nil && vueInfo.IsDir() && err2 == nil && sfcInfo.IsDir()
}

const vueCompileScript = `
var sfc = require("@vue/compiler-sfc");
var fs  = require("fs");
var crypto = require("crypto");

var filename = process.argv[1];
var source   = fs.readFileSync(filename, "utf8");

var parsed = sfc.parse(source, { filename: filename });
if (parsed.errors.length) {
  process.stderr.write(parsed.errors[0].message || String(parsed.errors[0]));
  process.exit(1);
}

var descriptor = parsed.descriptor;
var id      = crypto.createHash("sha256").update(filename).digest("hex").slice(0, 8);
var hasScoped = descriptor.styles.some(function(s) { return s.scoped; });
var scopeId   = hasScoped ? ("data-v-" + id) : undefined;

// --- Script block ---
var scriptCode = "const __sfc__ = {};";
if (descriptor.script || descriptor.scriptSetup) {
  var compiled = sfc.compileScript(descriptor, {
    id: id,
    inlineTemplate: !!descriptor.scriptSetup,
    templateOptions: { scopeId: scopeId },
  });
  scriptCode = sfc.rewriteDefault(compiled.content, "__sfc__");
}

// --- Template block (Options API: separate <script> + <template>) ---
var parts = [];
if (descriptor.template && !descriptor.scriptSetup) {
  var tpl = sfc.compileTemplate({
    source:   descriptor.template.content,
    filename: filename,
    id:       id,
    scopeId:  scopeId,
  });
  if (tpl.errors && tpl.errors.length) {
    process.stderr.write(String(tpl.errors[0]));
    process.exit(1);
  }
  var renderCode = tpl.code
    .replace(/\bexport function render\b/, "function render")
    .replace(/\nexport \{ render \};?/, "");
  parts.push(renderCode);
  scriptCode += "\n__sfc__.render = render;";
}

if (scopeId) {
  scriptCode += "\n__sfc__.__scopeId = " + JSON.stringify(scopeId) + ";";
}

parts.push(scriptCode);
parts.push("export default __sfc__;");
process.stdout.write(parts.join("\n"));
`

func VuePlugin(appDir string) api.Plugin {
	return api.Plugin{
		Name: "vue",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: `\.vue$`},
				func(args api.OnLoadArgs) (api.OnLoadResult, error) {
					rt, err := jsruntime.Require()
				if err != nil {
					return api.OnLoadResult{}, err
				}
				cmd := exec.Command(rt, "-e", vueCompileScript, args.Path)
					cmd.Dir = appDir
					out, err := cmd.CombinedOutput()
					if err != nil {
						return api.OnLoadResult{},
							fmt.Errorf("vue compile %s: %w\n%s", args.Path, err, out)
					}
					s := string(out)

					return api.OnLoadResult{Contents: &s, Loader: api.LoaderTS}, nil
				})
		},
	}
}
