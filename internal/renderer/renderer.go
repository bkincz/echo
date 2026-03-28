package renderer

import (
	"bytes"
	"html/template"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------
type ShellOptions struct {
	Title        string
	Description  string
	BundleURL    string
	CSSBundleURL string
	SSEURL       string
	DevMode      bool
}

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------
var shellTmpl = template.Must(template.New("shell").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>{{.Title}}</title>
  {{- if .Description}}
  <meta name="description" content="{{.Description}}" />
  {{- end}}
  {{- if .CSSBundleURL}}
  <link rel="stylesheet" href="{{.CSSBundleURL}}" />
  {{- end}}
</head>
<body>
  <div id="root"></div>
  <script type="module" src="{{.BundleURL}}"></script>
  {{- if .DevMode}}
  <script>
    // Echo dev runtime: hot reload + build error overlay.
    (function () {
      const overlay = document.createElement("div");
      overlay.style.cssText = "display:none;position:fixed;inset:0;background:#0d0d0d;color:#ff5555;font:14px/1.6 monospace;padding:32px;z-index:99999;white-space:pre-wrap;overflow:auto;";
      document.body.appendChild(overlay);

      const es = new EventSource({{printf "%q" .SSEURL}});
      es.onmessage = () => { overlay.style.display = "none"; location.reload(); };
      es.addEventListener("build_error", (e) => {
        overlay.style.display = "block";
        overlay.textContent = "\u26a0 Build Error\n\n" + JSON.parse(e.data).message;
      });
      es.onerror = () => { es.close(); setTimeout(() => location.reload(), 500); };
    })();
  </script>
  {{- end}}
</body>
</html>
`))

// ---------------------------------------------------------------------------
// Renderer
// ---------------------------------------------------------------------------
func Shell(opts ShellOptions) (string, error) {
	if opts.Title == "" {
		opts.Title = "Echo"
	}
	if opts.SSEURL == "" {
		opts.SSEURL = "/_echo/sse"
	}
	var buf bytes.Buffer
	if err := shellTmpl.Execute(&buf, opts); err != nil {
		return "", err
	}
	return buf.String(), nil
}
