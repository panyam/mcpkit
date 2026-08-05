package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var embeddedStatic embed.FS

// staticAssets returns the /static file tree. E3 ships a placeholder bundle; E4
// replaces the contents with the esbuild-built DockView + Solid frontend, no
// change to this serving code.
func staticAssets() fs.FS {
	sub, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		// The embed directive guarantees the directory exists, so this cannot
		// happen at runtime; return the root FS rather than panic.
		return embeddedStatic
	}
	return sub
}

// shellHandler serves the placeholder server-rendered shell. It is deliberately
// minimal: a page proving the endpoints exist, replaced by the templar-rendered
// DockView shell in E4. Non-root paths that fall through the mux 404 here.
func shellHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(shellHTML))
}

const shellHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>agentweb — mcpkit</title>
<style>
  body { font: 14px/1.5 system-ui, sans-serif; max-width: 46rem; margin: 3rem auto; padding: 0 1rem; }
  code { background: #f2f2f2; padding: 0 .25rem; border-radius: 3px; }
  ul { padding-left: 1.2rem; }
</style>
</head>
<body>
<h1>agentweb</h1>
<p>Connect bridge over the agent host (issue 1196). This is a placeholder shell;
the DockView + Solid frontend lands in E4 (issue 1197). The live surface is the
<code>mcpkit.agentweb.v1.HostService</code> Connect service:</p>
<ul>
  <li><code>Watch</code> — server-streaming host event log (backlog + live)</li>
  <li><code>Submit</code> — run a turn</li>
  <li><code>Dispatch</code> — run a slash command</li>
  <li><code>RespondToAsk</code> — answer a pending elicitation</li>
  <li><code>ListSessions</code> / <code>GetStatus</code> — host state reads</li>
</ul>
<p>Frontend assets are served from <code>/static/</code>.</p>
</body>
</html>
`
