package web

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed static
var embeddedStatic embed.FS

// staticAssets returns the /static file tree — the esbuild-built DockView + Solid
// frontend (agent/web/web builds into this directory, which is committed so a
// plain `go build` needs no Node step).
func staticAssets() fs.FS {
	sub, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		// The embed directive guarantees the directory exists, so this cannot
		// happen at runtime; return the root FS rather than panic.
		return embeddedStatic
	}
	return sub
}

// shellData is the server-rendered shell's template model. Layout is the chosen
// surface ("dockview" or "mobile"), stamped onto #workspace[data-layout] where
// main.ts reads it back; Dockview links the dockview stylesheet only on that
// layout (the mobile page never requests it).
type shellData struct {
	Layout   string
	Dockview bool
}

// shellTemplate is the goapplib/diffpp shell pattern rendered with the stdlib
// html/template (no goapplib/templar dependency for this one static page): a
// self-contained page that links the esbuild bundle from /static/ and lays out
// the island holes the frontend adopts. The CSS is inlined so the surface is one
// file to serve; it is theme-aware (light/dark via prefers-color-scheme and a
// forced data-theme). The DockView chrome (#dockview-container, #dock-menu) and
// the mobile root (#workspace itself) are the two holes main.ts mounts into.
var shellTemplate = template.Must(template.New("shell").Parse(shellHTML))

// shellHandler serves the server-rendered shell. The layout is chosen from the
// ?layout= query (default dockview; mobile is the narrow-screen variant), which
// is how the topbar's layout <select> switches surfaces. Non-root paths that
// fall through the mux 404 here.
func shellHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	layout := r.URL.Query().Get("layout")
	if layout != "mobile" {
		layout = "dockview"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = shellTemplate.Execute(w, shellData{Layout: layout, Dockview: layout == "dockview"})
}

const shellHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>agentweb — mcpkit</title>
{{if .Dockview}}<link rel="stylesheet" href="/static/dockview.css">{{end}}
<style>
:root {
  --bg: #ffffff; --fg: #1a1a1a; --muted: #6b7280; --line: #e5e7eb;
  --panel: #f8fafc; --accent: #2563eb; --user: #eef2ff; --sys: #fff7ed; --err: #b91c1c;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f1115; --fg: #e6e6e6; --muted: #9aa1ac; --line: #262b33;
    --panel: #161a20; --accent: #60a5fa; --user: #1b2130; --sys: #2a2015; --err: #f87171;
  }
}
:root[data-theme="light"] { --bg:#fff; --fg:#1a1a1a; --muted:#6b7280; --line:#e5e7eb; --panel:#f8fafc; --user:#eef2ff; --sys:#fff7ed; }
:root[data-theme="dark"] { --bg:#0f1115; --fg:#e6e6e6; --muted:#9aa1ac; --line:#262b33; --panel:#161a20; --user:#1b2130; --sys:#2a2015; }
* { box-sizing: border-box; }
html, body { height: 100%; margin: 0; }
body { font: 14px/1.5 system-ui, -apple-system, sans-serif; background: var(--bg); color: var(--fg); display: flex; flex-direction: column; }
.topbar { display: flex; align-items: center; gap: 1rem; padding: .5rem .9rem; border-bottom: 1px solid var(--line); background: var(--panel); }
.topbar-brand { font-weight: 600; letter-spacing: .2px; }
.topbar-status { color: var(--muted); font-size: 12px; flex: 1; }
.topbar-select { background: var(--bg); color: var(--fg); border: 1px solid var(--line); border-radius: 6px; padding: .2rem .4rem; }
#workspace { flex: 1; min-height: 0; display: flex; flex-direction: column; }
/* DockView chrome */
.ws-north { display: flex; align-items: center; justify-content: space-between; padding: .3rem .7rem; border-bottom: 1px solid var(--line); color: var(--muted); font-size: 12px; }
#dockview-container { flex: 1; min-height: 0; }
.ws-dock-panel { height: 100%; background: var(--bg); }
.dock-menu { position: relative; }
.dock-menu-btn { background: var(--bg); color: var(--fg); border: 1px solid var(--line); border-radius: 6px; padding: .15rem .5rem; cursor: pointer; }
.dock-menu-list { position: absolute; right: 0; top: 100%; margin-top: 4px; background: var(--panel); border: 1px solid var(--line); border-radius: 6px; min-width: 160px; z-index: 20; box-shadow: 0 6px 20px rgba(0,0,0,.15); }
.dock-menu-item { display: block; width: 100%; text-align: left; background: none; border: 0; color: var(--fg); padding: .4rem .6rem; cursor: pointer; }
.dock-menu-item:hover { background: var(--line); }
/* Conversation panel */
.conv { display: flex; flex-direction: column; height: 100%; }
.conv-log { flex: 1; overflow-y: auto; padding: 1rem; display: flex; flex-direction: column; gap: .8rem; }
.conv-turn { display: flex; flex-direction: column; gap: .2rem; max-width: 46rem; }
.conv-role { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); }
.conv-text { white-space: pre-wrap; padding: .5rem .7rem; border-radius: 8px; border: 1px solid var(--line); background: var(--panel); }
.conv-user { align-self: flex-end; align-items: flex-end; }
.conv-user .conv-text { background: var(--user); }
.conv-system .conv-text { background: var(--sys); font-size: 13px; }
.conv-streaming .conv-text { border-style: dashed; }
.conv-activity { color: var(--muted); font-size: 12px; font-style: italic; }
.conv-error { color: var(--err); font-size: 13px; }
.conv-from { color: var(--muted); font-size: 10px; text-transform: none; letter-spacing: 0; font-style: italic; }
.conv-resolved { display: flex; align-items: center; gap: .5rem; align-self: center; color: var(--muted); font-size: 12px; font-style: italic; padding: .1rem .2rem; }
.conv-resolved-dismiss { border: 0; background: none; color: var(--muted); cursor: pointer; font-size: 12px; line-height: 1; padding: 0 .2rem; }
.conv-ask { border-top: 1px solid var(--line); background: var(--sys); padding: .7rem 1rem; display: flex; flex-direction: column; gap: .5rem; }
.conv-ask-msg { font-weight: 500; }
.conv-ask-actions { display: flex; gap: .5rem; }
.conv-approve, .conv-decline { border: 1px solid var(--line); border-radius: 6px; padding: .3rem .8rem; cursor: pointer; }
.conv-approve { background: var(--accent); color: #fff; border-color: var(--accent); }
.conv-decline { background: var(--bg); color: var(--fg); }
.conv-compose { display: flex; gap: .5rem; align-items: flex-end; padding: .7rem 1rem; border-top: 1px solid var(--line); background: var(--panel); }
.conv-ta { flex: 1; resize: none; font: inherit; padding: .5rem .6rem; border: 1px solid var(--line); border-radius: 8px; background: var(--bg); color: var(--fg); }
.conv-send { width: 38px; height: 38px; border-radius: 50%; border: 0; background: var(--accent); color: #fff; font-size: 18px; cursor: pointer; }
.conv-send:disabled { opacity: .4; cursor: default; }
/* Observability panels (#1198) */
.obs { display: flex; flex-direction: column; height: 100%; min-height: 0; font-size: 13px; }
.obs-list { flex: 1; min-height: 0; overflow-y: auto; padding: .5rem .4rem; display: flex; flex-direction: column; gap: 2px; }
.obs-empty { color: var(--muted); font-size: 12px; padding: 1rem; line-height: 1.5; }
/* Sub-agent tree */
.sa-row { display: flex; align-items: baseline; gap: .5rem; padding: .2rem .4rem; border-radius: 6px; }
.sa-row:hover { background: var(--panel); }
.sa-dot { font-size: 11px; width: 1em; }
.sa-idle { color: var(--muted); }
.sa-running { color: var(--accent); }
.sa-done { color: #16a34a; }
.sa-error { color: var(--err); }
.sa-name { font-weight: 600; }
.sa-tools { font-size: 11px; color: var(--muted); border: 1px solid var(--line); border-radius: 999px; padding: 0 .4rem; }
.sa-activity { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* Timeline */
.tl-filter { display: flex; align-items: center; gap: .5rem; padding: .5rem .6rem; border-bottom: 1px solid var(--line); background: var(--panel); }
.tl-filter-label { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); }
.tl-select { background: var(--bg); color: var(--fg); border: 1px solid var(--line); border-radius: 6px; padding: .15rem .4rem; }
.tl-row { display: flex; gap: .5rem; align-items: baseline; padding: .15rem .4rem; }
.tl-kind { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; color: var(--accent); min-width: 9rem; }
.tl-summary { color: var(--fg); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* Memory */
.mem-head { display: flex; align-items: center; justify-content: space-between; padding: .5rem .6rem; border-bottom: 1px solid var(--line); background: var(--panel); }
.mem-title { font-weight: 600; }
.mem-refresh { background: var(--bg); color: var(--fg); border: 1px solid var(--line); border-radius: 6px; padding: .2rem .6rem; cursor: pointer; }
.mem-refresh:disabled { opacity: .5; cursor: default; }
.mem-summary { padding: .5rem .6rem; }
.mem-pre { white-space: pre-wrap; margin: 0; font-size: 12px; }
.mem-section-label { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); padding: .4rem .6rem 0; }
.mem-compactions { flex: 0 1 auto; }
.mem-row { display: flex; gap: .5rem; align-items: baseline; padding: .2rem .4rem; }
.mem-badge { font-size: 11px; color: var(--accent); border: 1px solid var(--line); border-radius: 999px; padding: 0 .4rem; }
.mem-saved { color: #16a34a; }
/* Tools & offload */
.tool-banner { padding: .35rem .6rem; background: var(--sys); color: var(--fg); font-size: 12px; border-bottom: 1px solid var(--line); }
.tool-row { border-bottom: 1px solid var(--line); }
.tool-head { display: flex; gap: .5rem; align-items: baseline; padding: .3rem .5rem; cursor: pointer; }
.tool-head:hover { background: var(--panel); }
.tool-status { font-size: 10px; text-transform: uppercase; letter-spacing: .3px; border-radius: 4px; padding: 0 .35rem; border: 1px solid var(--line); }
.tool-ok { color: #16a34a; }
.tool-error, .tool-unavailable { color: var(--err); }
.tool-running { color: var(--accent); }
.tool-denied, .tool-cancelled { color: var(--muted); }
.tool-name { font-weight: 600; }
.tool-offload { font-size: 11px; color: var(--accent); border: 1px solid var(--accent); border-radius: 999px; padding: 0 .4rem; }
.tool-preview { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tool-detail { padding: .2rem .7rem .5rem; }
.tool-detail-label { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); margin-top: .3rem; }
.tool-pre { white-space: pre-wrap; word-break: break-word; margin: .1rem 0 0; font-size: 12px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
/* Budget gauges */
.obs-budget { padding: .8rem .7rem; gap: .8rem; }
.bg-tiles { display: grid; grid-template-columns: repeat(2, 1fr); gap: .5rem; }
.bg-tile { border: 1px solid var(--line); border-radius: 10px; background: var(--panel); padding: .6rem; }
.bg-value { font-size: 20px; font-weight: 700; }
.bg-label { font-size: 11px; text-transform: uppercase; letter-spacing: .5px; color: var(--muted); }
.bg-bar-label, .bg-last { font-size: 12px; color: var(--muted); }
.bg-bar { display: flex; height: 12px; border-radius: 999px; overflow: hidden; border: 1px solid var(--line); }
.bg-bar-in { background: var(--accent); }
.bg-bar-out { background: #16a34a; }
.mobile-tiles { display: flex; flex-direction: column; gap: .6rem; }
/* Mobile overlay */
.mobile-root { position: relative; height: 100%; }
.mobile-home { padding: 1.2rem; }
.mobile-title { font-size: 20px; font-weight: 600; margin-bottom: 1rem; }
.mobile-launch { display: flex; flex-direction: column; align-items: flex-start; gap: .2rem; width: 100%; text-align: left; padding: 1rem; border: 1px solid var(--line); border-radius: 12px; background: var(--panel); color: var(--fg); cursor: pointer; }
.mobile-launch-name { font-weight: 600; }
.mobile-launch-sub { font-size: 12px; color: var(--muted); }
.mobile-overlay { position: absolute; inset: 0; display: flex; flex-direction: column; background: var(--bg); }
.mobile-overlay-bar { display: flex; align-items: center; justify-content: space-between; padding: .6rem .9rem; border-bottom: 1px solid var(--line); background: var(--panel); }
.mobile-overlay-title { font-weight: 600; }
.mobile-overlay-close { background: none; border: 0; font-size: 18px; color: var(--fg); cursor: pointer; }
.mobile-overlay-body { flex: 1; min-height: 0; }
</style>
</head>
<body>
<div class="topbar">
  <span class="topbar-brand">agentweb</span>
  <span class="topbar-status" id="status-line">connecting…</span>
  <label>Layout
    <select id="layout-select" class="topbar-select">
      <option value="dockview"{{if .Dockview}} selected{{end}}>Desktop (DockView)</option>
      <option value="mobile"{{if not .Dockview}} selected{{end}}>Mobile</option>
    </select>
  </label>
</div>
<div id="workspace" data-layout="{{.Layout}}">
{{if .Dockview}}
  <div class="ws-north"><span>Conversation workspace</span><div id="dock-menu" class="dock-menu"></div></div>
  <div id="dockview-container"></div>
{{end}}
</div>
<script type="module" src="/static/app.js"></script>
</body>
</html>
`
