/**
 * SEP-414 trace-context relay across the View ↔ host postMessage boundary.
 *
 * Shared by the mcpkit bridge (`MCPApp.setTraceContextProvider`) and the
 * `withTraceRelay` add-on for upstream's `App`, so both stamp `_meta` the same
 * way, and the same way as Go's `core.InjectTraceContextIntoParams`: a
 * `traceparent` or `tracestate` the caller already set is never overwritten.
 */

/**
 * W3C Trace Context (https://www.w3.org/TR/trace-context/) fields. An absent
 * `traceparent` means "do not propagate for this message".
 */
export interface TraceContext {
  /** W3C traceparent value (`00-<trace-id>-<span-id>-<flags>`). */
  traceparent?: string;
  /** W3C tracestate value (vendor-specific key=value pairs). */
  tracestate?: string;
}

/**
 * Called once per outbound message to ask for the current trace context.
 * Return null, undefined, or a context without `traceparent` to skip that
 * message. A throw is caught, logged, and treated as "skip".
 */
export type TraceContextProvider = () => TraceContext | null | undefined;

/**
 * Ask the provider for a trace context, returning null when there is nothing
 * to propagate. A provider that throws is logged with `logPrefix` and yields
 * null, so tracing can never break the message it would have stamped.
 */
export function resolveTraceContext(
  provider: TraceContextProvider | null | undefined,
  logPrefix = "[mcpkit]"
): TraceContext | null {
  if (!provider) return null;
  let tc: TraceContext | null | undefined;
  try {
    tc = provider();
  } catch (err) {
    if (typeof console !== "undefined") {
      console.warn(logPrefix + " traceContextProvider threw; skipping trace propagation:", err);
    }
    return null;
  }
  return tc && tc.traceparent ? tc : null;
}

/**
 * Return params with `tc` merged into `params._meta`. Values already present
 * in `_meta` win. Missing or null params become `{_meta}` so a no-argument
 * call still carries the trace. Arrays and scalars are returned unchanged,
 * since MCP's `_meta` convention only applies to object params.
 */
export function mergeTraceMeta(params: unknown, tc: TraceContext): unknown {
  let merged: Record<string, unknown>;
  if (params === undefined || params === null) {
    merged = {};
  } else if (typeof params === "object" && !Array.isArray(params)) {
    merged = { ...(params as Record<string, unknown>) };
  } else {
    return params;
  }
  const existing = merged._meta;
  const meta: Record<string, unknown> =
    existing && typeof existing === "object" && !Array.isArray(existing)
      ? { ...(existing as Record<string, unknown>) }
      : {};
  if (meta.traceparent === undefined && tc.traceparent) meta.traceparent = tc.traceparent;
  if (meta.tracestate === undefined && tc.tracestate) meta.tracestate = tc.tracestate;
  merged._meta = meta;
  return merged;
}

/** Anything with the `send` method of an MCP transport. */
export interface SendingTransport {
  send(message: any, options?: any): unknown;
}

/**
 * Make `transport` stamp trace context onto every outbound JSON-RPC request
 * and notification (any message with a `method`). Responses pass through
 * untouched. Returns the same transport, with its `send` replaced, so the
 * callbacks `App` installs on it after `connect` keep working:
 *
 *     const transport = withTraceRelay(new PostMessageTransport(window.parent, window.parent), provider);
 *     await app.connect(transport);
 *
 * Wrap before `connect`. Calling it twice on one transport stamps twice,
 * which is harmless because existing values win.
 */
export function withTraceRelay<T extends SendingTransport>(
  transport: T,
  provider: TraceContextProvider
): T {
  const send = transport.send.bind(transport);
  transport.send = ((message: any, options?: any) => {
    if (message && typeof message === "object" && typeof message.method === "string") {
      const tc = resolveTraceContext(provider, "[mcpkit withTraceRelay]");
      if (tc) {
        message = { ...message, params: mergeTraceMeta(message.params, tc) };
      }
    }
    return send(message, options);
  }) as T["send"];
  return transport;
}
