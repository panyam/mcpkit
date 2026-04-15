package ui

import "html/template"

// BridgeData is the template data for the "mcpkit-bridge" template.
// Use with html/template — BridgeJS is typed as template.JS so it
// passes through without escaping; string fields are auto-escaped.
type BridgeData struct {
	AppName         string      // App name for ui/initialize handshake
	AppVersion      string      // App version
	ProtocolVersion string      // MCP Apps spec version (default: "2026-01-26")
	BridgeJS        template.JS // The bridge runtime JS (trusted, unescaped)
}

// NewBridgeData creates BridgeData with the bridge JS pre-loaded.
// ProtocolVersion defaults to "2026-01-26" (current MCP Apps spec).
func NewBridgeData(appName, appVersion string) BridgeData {
	return BridgeData{
		AppName:         appName,
		AppVersion:      appVersion,
		ProtocolVersion: "2026-01-26",
		BridgeJS:        template.JS(AppBridgeScript),
	}
}

// BridgeTemplateDef returns the raw text of the "mcpkit-bridge" named
// template definition. Parse it into your template set:
//
//	tmpl := template.Must(template.New("page").Parse(pageHTML))
//	template.Must(tmpl.Parse(ui.BridgeTemplateDef()))
//
// Then use it in your HTML:
//
//	{{ template "mcpkit-bridge" .Bridge }}
func BridgeTemplateDef() string {
	return bridgeTemplateText
}
