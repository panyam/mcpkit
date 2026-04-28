/**
 * React hook for the MCP App Bridge.
 *
 * This is the thin wrapper that makes MCPApp work naturally in React.
 * ~30 lines — demonstrates that React hooks are a trivial addon,
 * not a reason to pull in the full upstream ext-apps SDK.
 */

import { useState, useEffect, useCallback, useRef } from "react";

/** Hook that tracks bridge connection state and host context. */
export function useMCPApp() {
  const [connected, setConnected] = useState(MCPApp.connected);
  const [hostContext, setHostContext] = useState(MCPApp.hostContext);

  useEffect(() => {
    const unsubConnect = MCPApp.on("connected", (data) => {
      setConnected(true);
      setHostContext(data.hostContext);
    });
    const unsubContext = MCPApp.on("hostcontextchanged", (data) => {
      setHostContext(data.hostContext);
    });
    return () => { unsubConnect(); unsubContext(); };
  }, []);

  const callTool = useCallback(
    (name: string, args?: Record<string, unknown>, options?: RequestOptions) =>
      MCPApp.callTool(name, args, options),
    []
  );

  return { connected, hostContext, callTool };
}

/** Hook that subscribes to a bridge event. */
export function useMCPEvent<E extends MCPAppEvent>(
  event: E,
  handler: (data: MCPAppEventMap[E]) => void
) {
  useEffect(() => {
    return MCPApp.on(event, handler);
  }, [event, handler]);
}

/**
 * Hook for declarative app-provided tool registration.
 * Registers the tool on mount and removes it on unmount.
 * Handler updates automatically when it changes (via ref).
 *
 * Usage:
 *   useMCPAppTool("get_count", { description: "Get counter" }, () => ({
 *     content: [{ type: "text", text: String(count) }]
 *   }));
 */
export function useMCPAppTool(
  name: string,
  config: { description?: string; inputSchema?: unknown; outputSchema?: unknown },
  handler: (args: Record<string, unknown>) => unknown | Promise<unknown>
) {
  const handlerRef = useRef(handler);
  handlerRef.current = handler;

  useEffect(() => {
    const handle = MCPApp.registerTool(name, config, (args) =>
      handlerRef.current(args)
    );
    return () => handle.remove();
    // Re-register if name or config identity changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);
}
