/**
 * Type declarations for mcp-app-extras.js, mcpkit's View-side extras for
 * any MCP Apps runtime, including upstream's `App` from
 * `@modelcontextprotocol/ext-apps`. Hand-maintained alongside
 * mcp-app-extras.ts, like mcp-app-bridge.d.ts.
 */

/**
 * SEP-2356 file picker descriptor. Mirrors the server-side
 * `core.FileInputDescriptor`. The picker enforces `accept` and `maxSize`
 * before encoding, so a server receives only files it declared it accepts.
 */
export interface FileInputDescriptor {
  /** MIME types (`image/png`, `image/*`) or extensions (`.csv`). Empty accepts anything. */
  accept?: string[];
  /** Maximum size in bytes. */
  maxSize?: number;
}

/** The user dismissed the picker without choosing a file. */
export declare class MCPFileSelectionCanceled extends Error {}

/** The chosen file is larger than the descriptor's `maxSize`. */
export declare class MCPFileTooLarge extends Error {
  readonly reason: "file_too_large";
  readonly size: number;
  readonly maxSize: number;
}

/** The chosen file matches none of the descriptor's `accept` entries. */
export declare class MCPFileTypeNotAccepted extends Error {
  readonly reason: "file_type_not_accepted";
  readonly mediaType: string;
  readonly accept: ReadonlyArray<string>;
}

/**
 * Open a native file picker and resolve with the chosen file as an RFC 2397
 * data URI (`data:<mediaType>;name=<pct-encoded>;base64,<payload>`), the form
 * `core.DecodeDataURI` reads. Must be called from a user-gesture handler.
 */
export declare function selectFile(descriptor?: FileInputDescriptor): Promise<string>;

/** Multi-select variant of selectFile, resolving in selection order. */
export declare function selectFiles(descriptor?: FileInputDescriptor): Promise<string[]>;

/** W3C Trace Context fields. An absent `traceparent` skips propagation. */
export interface TraceContext {
  traceparent?: string;
  tracestate?: string;
}

/**
 * Called once per outbound message. Return null, undefined, or a context
 * without `traceparent` to skip that message. A throw is logged and skipped.
 */
export type TraceContextProvider = () => TraceContext | null | undefined;

/** Anything with the `send` method of an MCP transport. */
export interface SendingTransport {
  send(message: any, options?: any): unknown;
}

/**
 * Make `transport` stamp `_meta.traceparent` / `tracestate` onto every
 * outbound JSON-RPC request and notification. Values the caller already set
 * win, matching Go's `core.InjectTraceContextIntoParams`. Responses pass
 * through. Returns the same transport with its `send` replaced, so wrap it
 * before `app.connect(transport)`.
 */
export declare function withTraceRelay<T extends SendingTransport>(
  transport: T,
  provider: TraceContextProvider
): T;
