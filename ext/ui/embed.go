package ui

import _ "embed"

// AppBridgeScript is the compiled MCP App Bridge JS, ready to inject
// into HTML via InjectAppBridge or AppShellHTML. Source: assets/mcp-app-bridge.ts.
//
//go:embed assets/mcp-app-bridge.js
var AppBridgeScript string
