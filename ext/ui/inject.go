package ui

import (
	"bytes"
	"fmt"
	"text/template"
	"net/http"
	"strings"
)

// bridgeSentinel is embedded in the injected script block so
// InjectAppBridge can detect a prior injection and stay idempotent.
const bridgeSentinel = "/* mcpkit:app-bridge */"

// BridgeConfig customizes the bridge's app identity sent during the
// ui/initialize handshake with the host.
type BridgeConfig struct {
	// App name sent as appInfo.name in ui/initialize. Default: "mcp-app".
	Name string
	// App version sent as appInfo.version. Default: "0.0.0".
	Version string
	// Protocol version. Default: "2026-01-26" (current MCP Apps spec).
	ProtocolVersion string
}

func (c *BridgeConfig) name() string {
	if c != nil && c.Name != "" {
		return c.Name
	}
	return "mcp-app"
}

func (c *BridgeConfig) version() string {
	if c != nil && c.Version != "" {
		return c.Version
	}
	return "0.0.0"
}

func (c *BridgeConfig) protocol() string {
	if c != nil && c.ProtocolVersion != "" {
		return c.ProtocolVersion
	}
	return "2026-01-26"
}

// configScript generates the inline <script> that sets window.MCPAppConfig.
func configScript(cfg *BridgeConfig) string {
	return fmt.Sprintf(
		"<script>\n  window.MCPAppConfig = {\n    name: %q,\n    version: %q,\n    protocolVersion: %q\n  };\n</script>",
		cfg.name(), cfg.version(), cfg.protocol(),
	)
}

// InjectAppBridge inserts the MCP App Bridge <script> block before
// </body> in the provided HTML. If no </body> is found, the script
// is appended to the end.
//
// An optional BridgeConfig sets the app identity for the ui/initialize
// handshake. Pass nil to use the bridge's built-in defaults.
//
// The call is idempotent — if the sentinel comment is already present
// in html, the original string is returned unchanged.
func InjectAppBridge(html string, cfg ...*BridgeConfig) string {
	if strings.Contains(html, bridgeSentinel) {
		return html
	}

	var c *BridgeConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}

	tag := "\n" + configScript(c) + "\n" +
		`<script type="module">` + bridgeSentinel + "\n" + AppBridgeScript + "\n</script>\n"

	// Case-insensitive search for </body>.
	lower := strings.ToLower(html)
	if i := strings.LastIndex(lower, "</body>"); i >= 0 {
		return html[:i] + tag + html[i:]
	}
	return html + tag
}

// shellTemplate is the parsed HTML template for AppShellHTML.
var shellTemplate = template.Must(template.New("shell").Parse(shellTemplateHTML))

// shellData is the data passed to the shell template.
type shellData struct {
	Title          string
	BodyHTML       string
	ConfigName     string
	ConfigVersion  string
	ConfigProtocol string
	BridgeScript   string
}

// AppShellHTML generates a minimal HTML5 document with the bridge
// pre-injected. Use this in resource handlers that build HTML
// programmatically rather than from template files.
//
// An optional BridgeConfig sets the app identity for the ui/initialize
// handshake. Pass nil to use defaults.
func AppShellHTML(title string, bodyHTML string, cfg ...*BridgeConfig) string {
	var c *BridgeConfig
	if len(cfg) > 0 {
		c = cfg[0]
	}

	data := shellData{
		Title:          title,
		BodyHTML:       bodyHTML,
		ConfigName:     c.name(),
		ConfigVersion:  c.version(),
		ConfigProtocol: c.protocol(),
		BridgeScript:   AppBridgeScript,
	}

	var buf bytes.Buffer
	if err := shellTemplate.Execute(&buf, data); err != nil {
		// Template is compiled at init time, so this should never fail.
		panic("mcpkit: shell template execution failed: " + err.Error())
	}
	return buf.String()
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
