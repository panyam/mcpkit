import { errorResult, toCallToolResult, type PageTool } from "./tool.js";

/**
 * The part of an MCP Apps bridge `registerOnApp` needs: mcpkit's
 * `window.MCPApp` (ext/ui's mcp-app-bridge) has this shape.
 */
export interface AppToolRegistry {
  registerTool(
    name: string,
    config: { description?: string; inputSchema?: unknown },
    handler: (args: Record<string, unknown>) => unknown | Promise<unknown>,
  ): unknown;
}

/**
 * Registers a PageTool with an MCP Apps bridge, so the same tool object an
 * app frame offers its host can be offered to a room with `joinToolRoom`.
 * Returns whatever the bridge's `registerTool` returns (ext/ui's handle has
 * `remove()`).
 *
 * The app bridge has no per-call cancellation, so `context.signal` is never
 * aborted here.
 */
export function registerOnApp(app: AppToolRegistry, tool: PageTool): unknown {
  return app.registerTool(
    tool.name,
    { description: tool.description, inputSchema: tool.inputSchema ?? { type: "object" } },
    async (args) => {
      try {
        return toCallToolResult(await tool.execute(args ?? {}, { signal: new AbortController().signal, progress() {}, from: "app-host" }));
      } catch (err) {
        return errorResult(err);
      }
    },
  );
}
