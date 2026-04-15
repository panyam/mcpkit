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
  styles?: {
    variables?: Record<string, string>;
    css?: { fonts?: string };
  };
  safeAreaInsets?: { top: number; right: number; bottom: number; left: number };
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

/** Options for request methods. */
interface RequestOptions {
  /** AbortSignal for cancellation. */
  signal?: AbortSignal;
  /** Timeout in milliseconds (shorthand for AbortSignal.timeout). */
  timeout?: number;
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

/** Handler for incoming tool calls from the host. */
type CallToolHandler = (params: {
  name: string;
  arguments: Record<string, unknown>;
}) => unknown | Promise<unknown>;

/** Handler for incoming list-tools requests from the host. */
type ListToolsHandler = () =>
  | Array<{ name: string; description?: string; inputSchema?: unknown }>
  | Promise<
      Array<{ name: string; description?: string; inputSchema?: unknown }>
    >;

/** The global MCPApp bridge singleton. */
interface MCPAppBridge {
  /** True after the ui/initialize handshake completes with the host. */
  readonly connected: boolean;

  /** Host context (theme, locale, dimensions, styles) from initialization. */
  readonly hostContext: HostContext | null;

  /** Host capabilities from initialization. */
  readonly hostCapabilities: Record<string, unknown> | null;

  // --- Events ---

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

  // --- Host-bound methods ---

  /** Call a tool on the MCP server (proxied through the host). */
  callTool(
    name: string,
    args?: Record<string, unknown>,
    options?: RequestOptions
  ): Promise<ToolCallResult>;

  /** Read a resource from the MCP server. */
  readResource(uri: string, options?: RequestOptions): Promise<ResourceReadResult>;

  /** Send a message to the conversation. */
  sendMessage(message: unknown, options?: RequestOptions): Promise<unknown>;

  /** Update the model context visible to the LLM. */
  updateModelContext(context: unknown, options?: RequestOptions): Promise<unknown>;

  /** Open a URL in the host browser (not inside the iframe). */
  openLink(url: string, options?: RequestOptions): Promise<unknown>;

  /** Initiate a file download through the host. */
  downloadFile(url: string, filename?: string, options?: RequestOptions): Promise<unknown>;

  /** Request a display mode change (inline, fullscreen, pip). */
  requestDisplayMode(mode: string, options?: RequestOptions): Promise<unknown>;

  /** Request app teardown. */
  requestTeardown(): void;

  /** Send a log message to the host. */
  log(level: string, message: string, data?: unknown): void;

  // --- Style utilities ---

  /** Apply theme to document root (sets data-theme + color-scheme). */
  applyTheme(theme: string): void;

  /** Apply CSS custom properties from host styles. */
  applyStyleVariables(
    variables: Record<string, string>,
    root?: HTMLElement
  ): void;

  /** Inject host font CSS (idempotent — only injects once). */
  applyFonts(fontCss: string): void;

  /** Apply all available styles from a host context (theme + variables + fonts). */
  applyHostStyles(ctx: HostContext): void;

  // --- Bidirectional handlers (host → app) ---

  /** Handler for tools/call requests from the host. */
  oncalltool: CallToolHandler | null;

  /** Handler for tools/list requests from the host. */
  onlisttools: ListToolsHandler | null;

  // --- Utility ---

  /** Returns true if running inside an MCP Apps host. */
  isHosted(): boolean;
}

declare var MCPApp: MCPAppBridge;
