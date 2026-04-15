package ui

import _ "embed"

// AppBridgeScript is the compiled MCP App Bridge JS.
// Source: assets/mcp-app-bridge.ts.
//
//go:embed assets/mcp-app-bridge.js
var AppBridgeScript string

// bridgeTemplateText is the Go template that renders the bridge
// as a single <script type="module"> block with config baked in.
//
//go:embed assets/bridge.tmpl
var bridgeTemplateText string

// shellTemplateHTML is the HTML template used by AppShellHTML.
//
//go:embed assets/shell.html
var shellTemplateHTML string
