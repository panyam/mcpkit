package ui

import (
	"net/http"
	"strings"
)

// bridgeSentinel is embedded in the injected script block so
// InjectAppBridge can detect a prior injection and stay idempotent.
const bridgeSentinel = "/* mcpkit:app-bridge */"

// InjectAppBridge inserts the MCP App Bridge <script> block before
// </body> in the provided HTML. If no </body> is found, the script
// is appended to the end.
//
// The call is idempotent — if the sentinel comment is already present
// in html, the original string is returned unchanged.
func InjectAppBridge(html string) string {
	if strings.Contains(html, bridgeSentinel) {
		return html
	}
	tag := "\n<script>" + bridgeSentinel + "\n" + AppBridgeScript + "\n</script>\n"

	// Case-insensitive search for </body>.
	lower := strings.ToLower(html)
	if i := strings.LastIndex(lower, "</body>"); i >= 0 {
		return html[:i] + tag + html[i:]
	}
	return html + tag
}

// BridgePath is the default URL path used by ServeBridge.
const BridgePath = "/_mcpkit/mcp-app-bridge.js"

// ServeBridge returns an http.Handler that serves the compiled bridge JS.
// Mount it on your mux alongside MCP and REST handlers:
//
//	mux.Handle(ui.BridgePath, ui.ServeBridge())
//
// HTML can then reference it via <script src="/_mcpkit/mcp-app-bridge.js">.
// Note: sandboxed iframes need the serving origin in their CSP connect-src
// or resource-src for this to work. For simplest setup, use InjectAppBridge
// to inline the script instead.
func ServeBridge() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write([]byte(AppBridgeScript))
	})
}

// AppShellHTML generates a minimal HTML5 document with the bridge
// pre-injected. Use this in resource handlers that build HTML
// programmatically rather than from template files.
func AppShellHTML(title string, bodyHTML string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>`)
	b.WriteString(title)
	b.WriteString(`</title>
<style>*{box-sizing:border-box}body{margin:0;font-family:system-ui}</style>
</head><body>
`)
	b.WriteString(bodyHTML)
	b.WriteString("\n<script>")
	b.WriteString(bridgeSentinel)
	b.WriteString("\n")
	b.WriteString(AppBridgeScript)
	b.WriteString("\n</script>\n</body></html>\n")
	return b.String()
}
