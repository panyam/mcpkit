# React App — MCP App with mcpkit backend

A React 19 MCP App that mirrors the upstream ext-apps `basic-server-vanillajs` feature set. Demonstrates that mcpkit works as a drop-in Go backend for React frontends.

## What it demonstrates

- React hooks for the bridge: `useMCPApp()`, `useMCPEvent()` (~30 lines)
- Type-safe via `mcp-app-bridge.d.ts` — full autocomplete for `MCPApp.*`
- Vite + `vite-plugin-singlefile` builds to one HTML file (same as upstream pattern)
- Go server injects bridge via `ui.InjectAppBridge()` into Vite-built output
- Tool calls, message sending, logging, link opening, theme adaptation

## Setup

```bash
# 1. Build the React frontend
cd examples/apps/react
pnpm install
pnpm build

# 2. Start the Go server
cd server
go run . -addr :8080
```

## Connect a host

In MCPJam (or Claude Desktop):
1. Add server: `http://localhost:8080/mcp` (Streamable HTTP)
2. Server name: "React App"

## Prompts to try

- "What time is it?" — calls `get-time` tool, time appears in the React UI
- Then click **Get Server Time** in the iframe — calls the tool back through the bridge
- Type a message and click **Send Message** — sends text to the conversation
- Click **Open Link** — opens the URL in the host browser (not the iframe)

## Key files

| File | What |
|------|------|
| `src/App.tsx` | React component: time display, message/log/link controls |
| `src/useMCPApp.ts` | React hooks: `useMCPApp()` + `useMCPEvent()` |
| `src/mcp-app-bridge.d.ts` | TypeScript declarations for `MCPApp` global |
| `server/main.go` | Go server: `get-time` tool, serves Vite-built HTML |
| `vite.config.ts` | Vite + singlefile plugin config |
