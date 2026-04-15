package ui

import _ "embed"

// AppBridgeScript is the compiled MCP App Bridge JS, ready to inject
// into HTML via InjectAppBridge or AppShellHTML. Source: assets/mcp-app-bridge.ts.
//
//go:embed assets/mcp-app-bridge.js
var AppBridgeScript string

// shellTemplateHTML is the HTML template used by AppShellHTML.
//
//go:embed assets/shell.html
var shellTemplateHTML string
