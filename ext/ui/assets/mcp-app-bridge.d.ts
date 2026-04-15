/**
 * Type declarations for the MCP App Bridge.
 *
 * These types describe the global MCPApp singleton exposed by
 * mcp-app-bridge.js. Use this file for TypeScript consumers building
 * MCP App frontends (React, Vue, Svelte, vanilla TS, etc.) against
 * a mcpkit Go backend.
 *
 * Usage: reference in tsconfig.json or use a triple-slash directive:
 *   /// <reference path="mcp-app-bridge.d.ts" />
 */

/** Event names emitted by the bridge. */
type MCPAppEvent =
  | "connected"
  | "toolinput"
  | "toolinputpartial"
  | "toolresult"
  | "toolcancelled"
  | "hostcontextchanged"
  | "teardown";

/** Host context provided during initialization and context-change events. */
interface HostContext {
  theme?: string;
  locale?: string;
  dimensions?: { width: number; height: number };
  [key: string]: unknown;
}

/** Payload delivered with each event. */
interface MCPAppEventMap {
  connected: { hostContext: HostContext; capabilities: Record<string, unknown> };
  toolinput: { tool: string; arguments: Record<string, unknown> };
  toolinputpartial: { tool: string; arguments: Record<string, unknown> };
  toolresult: { tool: string; result: unknown };
  toolcancelled: { tool: string };
  hostcontextchanged: { hostContext: HostContext };
  teardown: Record<string, never>;
}

/** Result from callTool(). */
interface ToolCallResult {
  content?: Array<{ type: string; text?: string; [k: string]: unknown }>;
  isError?: boolean;
  [key: string]: unknown;
}

/** Result from readResource(). */
interface ResourceReadResult {
  contents?: Array<{
    uri: string;
    mimeType?: string;
    text?: string;
    blob?: string;
    [k: string]: unknown;
  }>;
  [key: string]: unknown;
}

/** The global MCPApp bridge singleton. */
interface MCPAppBridge {
  /** True after the ui/initialize handshake completes with the host. */
  readonly connected: boolean;

  /** Host context (theme, locale, dimensions) from initialization. */
  readonly hostContext: HostContext | null;

  /** Host capabilities from initialization. */
  readonly hostCapabilities: Record<string, unknown> | null;

  /** Subscribe to an event. Returns an unsubscribe function. */
  on<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): () => void;

  /** Remove a previously registered handler. */
  off<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): void;

  /** Subscribe to an event, firing only once. Returns unsubscribe function. */
  once<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): () => void;

  /** Call a tool on the MCP server (proxied through the host). */
  callTool(
    name: string,
    args?: Record<string, unknown>
  ): Promise<ToolCallResult>;

  /** Read a resource from the MCP server. */
  readResource(uri: string): Promise<ResourceReadResult>;

  /** Send a message to the conversation. */
  sendMessage(message: unknown): Promise<unknown>;

  /** Update the model context visible to the LLM. */
  updateModelContext(context: unknown): Promise<unknown>;

  /** Open a URL in the host browser (not inside the iframe). */
  openLink(url: string): void;

  /** Initiate a file download through the host. */
  downloadFile(url: string, filename?: string): void;

  /** Request a display mode change (inline, fullscreen, pip). */
  requestDisplayMode(mode: string): Promise<unknown>;

  /** Request app teardown. */
  requestTeardown(): void;

  /** Send a log message to the host. */
  log(level: string, message: string, data?: unknown): void;

  /** Returns true if running inside an MCP Apps host. */
  isHosted(): boolean;
}

declare var MCPApp: MCPAppBridge;
