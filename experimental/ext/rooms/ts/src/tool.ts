/**
 * The tool shape the room client takes. It follows WebMCP's `registerTool`
 * descriptor (name, description, inputSchema, annotations, execute), so a
 * tool written for WebMCP registers here unchanged and the reverse.
 */
export interface PageTool {
  /** The tool's name as agents see it. `page` is reserved by the bridge. */
  name: string;
  description?: string;
  /** JSON Schema for the arguments. Defaults to `{type: "object"}`. */
  inputSchema?: Record<string, unknown>;
  /**
   * MCP tool annotations. `readOnlyHint: true` lets an agent holding only
   * `pages:read` call the tool; anything else needs `pages:act`.
   */
  annotations?: { readOnlyHint?: boolean; [key: string]: unknown };
  /**
   * Runs the tool. The result may be a full MCP `CallToolResult`, a string
   * (sent as one text block), or any other JSON value (sent as text and as
   * `structuredContent`). A thrown error becomes an `isError` result.
   */
  execute(args: Record<string, unknown>, context: ToolCallContext): unknown | Promise<unknown>;
}

/**
 * What a call gets besides its arguments. Every field is optional so a tool
 * written for a host that passes something else (WebMCP passes its own
 * client object) still type-checks.
 */
export interface ToolCallContext {
  /** Aborted when the agent cancels the call or the room is left. */
  signal?: AbortSignal;
  /** Reports progress to the agent. A no-op when the agent asked for none. */
  progress?(progress: number, total?: number, message?: string): void;
  /** The peer id of the bridge that forwarded the call. */
  from?: string;
}

/** An MCP `CallToolResult`, the subset the client produces. */
export interface CallToolResult {
  content: Array<{ type: string; text?: string; [key: string]: unknown }>;
  structuredContent?: unknown;
  isError?: boolean;
  [key: string]: unknown;
}

/** Turns whatever `execute` returned into a `CallToolResult`. */
export function toCallToolResult(value: unknown): CallToolResult {
  if (typeof value === "string") return { content: [{ type: "text", text: value }] };
  if (value && typeof value === "object" && Array.isArray((value as CallToolResult).content)) {
    return value as CallToolResult;
  }
  if (value === undefined) return { content: [] };
  return { content: [{ type: "text", text: JSON.stringify(value) }], structuredContent: value };
}

/** The `isError` result a thrown error becomes. */
export function errorResult(err: unknown): CallToolResult {
  const message = err instanceof Error ? err.message : String(err);
  return { content: [{ type: "text", text: message }], isError: true };
}
