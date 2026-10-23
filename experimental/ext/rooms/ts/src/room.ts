import { fromJson, type JsonValue } from "@bufbuild/protobuf";
import { base64Encode } from "@bufbuild/protobuf/wire";
import { CollabEventSchema, type CollabEvent } from "@panyam/massrelay";
import { GRPCWSClient } from "@panyam/servicekit-client";
import { errorResult, toCallToolResult, type PageTool } from "./tool.js";

/** The AppMessage kind that carries MCP JSON-RPC between a page and the bridge. */
export const MESSAGE_KIND = "mcp/1";

// Methods beyond MCP's own, mirroring experimental/ext/rooms/wire.go.
const PAGE_STATE = "notifications/rooms/page_state";
const AGENTS_ALLOWED = "notifications/rooms/agents_allowed";
const ACCEPT_PAIRING = "notifications/rooms/accept_pairing";
const REVOKE = "notifications/rooms/revoke";
const PAIRING_REQUESTED = "notifications/rooms/pairing_requested";
const METHOD_NOT_FOUND = -32601;
const INVALID_PARAMS = -32602;

/** A session asking to drive the page, as the bridge describes it. */
export interface PairingRequest {
  session: string;
  /** The session's subject, from its validated token. */
  subject: string;
  /** The agent client that asked, when it said. */
  client?: string;
  /** True when it is the page's own user, so the agents switch is enough. */
  sameSubject: boolean;
}

/** Where `registerTool` also registers when `modelContext` is set. */
export interface ModelContextLike {
  registerTool(tool: PageTool): unknown;
}

/** Options for {@link joinToolRoom}. */
export interface JoinToolRoomOptions {
  /** The relay's base URL: http(s) or ws(s), with any path it is mounted under. */
  relay: string;
  /** The room, which is the resource the page shows, e.g. `design:demo/board.kicad_sch`. */
  room: string;
  /**
   * The relay token the app minted for its user. The relay validates it and
   * reports its `sub` as the page's user, which is what pairing compares.
   * Sent as the `token` query parameter, since a browser WebSocket cannot set
   * headers.
   */
  token?: string;
  /** The name other peers see. Pairing never reads it. */
  username?: string;
  /**
   * Also register each tool with the browser's own WebMCP context, so an
   * agent built into the browser reaches the same handlers. `true` looks for
   * `navigator.modelContext`, then `document.modelContext`; or pass the
   * object. Missing is not an error.
   */
  modelContext?: boolean | ModelContextLike;
  /** A WebSocket implementation, for runtimes without a global one. */
  WebSocket?: typeof WebSocket;
  /** How long to wait for the relay to admit the page. Default 10s. */
  joinTimeoutMs?: number;
}

/** A page's membership in a tool room. */
export interface ToolRoom {
  /** The page's peer id, which agents pass as a tool's `page` argument. */
  readonly id: string;
  /** The room joined. */
  readonly room: string;
  /** Resolves when the page has left, by `leave()` or the relay dropping it. */
  readonly closed: Promise<void>;
  /** Adds or replaces a tool and tells the bridges the list changed. */
  registerTool(tool: PageTool): { unregister(): void };
  /** Removes a tool and tells the bridges the list changed. */
  unregisterTool(name: string): void;
  /** Sets what `list_pages` shows for this page. */
  setLabel(label: string): void;
  /** Reports that the page's user is looking at it now. */
  focus(): void;
  /**
   * The "let my agents drive this tab" switch, off by default. On pairs the
   * page with every agent session acting for the page's own user.
   */
  setAgentsAllowed(allowed: boolean): void;
  /** Accepts the code an agent session got back from `attach`. */
  acceptPairing(code: string): void;
  /** Ends the pairing with one session, or with all of them when omitted. */
  revoke(session?: string): void;
  /** Called when a session asks to drive the page. */
  onPairingRequest(fn: (req: PairingRequest) => void): void;
  /** Leaves the room. Calls in flight are aborted. */
  leave(): void;
}

interface RpcMessage {
  jsonrpc?: string;
  id?: string | number;
  method?: string;
  params?: any;
  result?: unknown;
  error?: { code: number; message: string };
}

/**
 * Joins a room on a massrelay relay as a page that offers tools to agents
 * through the mcpkit room bridge. Resolves once the relay admits the page,
 * and rejects with the relay's code (`ROOM_FULL`, `ROOM_FORBIDDEN`, ...) or
 * the socket's failure otherwise.
 *
 * The page answers `tools/list` and `tools/call` from any bridge in the room,
 * sends `notifications/tools/list_changed` when its tools change, and aborts a
 * call's `signal` on `notifications/cancelled`. It reads only messages
 * addressed to it, never room-wide ones.
 */
export async function joinToolRoom(opts: JoinToolRoomOptions): Promise<ToolRoom> {
  // servicekit's GRPCWSClient rather than massrelay's CollabClient: as of
  // massrelay 0.2.3 CollabClient treats every ErrorEvent as fatal, including
  // the PEER_NOT_FOUND the relay answers to a reply addressed to a bridge that
  // has just left, and closes without calling onDisconnect, so the page would
  // drop out of the room unnoticed.
  const grpc = new GRPCWSClient(opts.WebSocket ? { WebSocket: opts.WebSocket } : undefined);
  const page = new Page(grpc, opts);
  await page.join();
  return page;
}

class Page implements ToolRoom {
  id = "";
  readonly room: string;
  readonly closed: Promise<void>;
  private resolveClosed!: () => void;

  private tools = new Map<string, PageTool>();
  private native = new Map<string, unknown>();
  private bridges = new Set<string>();
  private running = new Map<string, AbortController>();
  private label = "";
  private allowed = false;
  private onPairing?: (req: PairingRequest) => void;
  private left = false;
  private modelContext?: ModelContextLike;

  constructor(private grpc: GRPCWSClient, private opts: JoinToolRoomOptions) {
    this.room = opts.room;
    this.closed = new Promise((resolve) => (this.resolveClosed = resolve));
    this.modelContext = findModelContext(opts.modelContext);
  }

  join(): Promise<void> {
    return new Promise((resolve, reject) => {
      let joined = false;
      const timer = setTimeout(() => {
        if (!joined) {
          reject(new Error(`joinToolRoom: relay did not admit the page within ${this.opts.joinTimeoutMs ?? 10000}ms`));
          this.grpc.close();
        }
      }, this.opts.joinTimeoutMs ?? 10000);
      this.grpc.onMessage = (data: unknown) => {
        let event: CollabEvent;
        try {
          event = fromJson(CollabEventSchema, data as JsonValue, { ignoreUnknownFields: true });
        } catch {
          return;
        }
        if (!joined) {
          if (event.event.case === "roomJoined") {
            joined = true;
            clearTimeout(timer);
            this.id = event.event.value.clientId;
            resolve();
          } else if (event.event.case === "error") {
            clearTimeout(timer);
            reject(new Error(`joinToolRoom: ${event.event.value.code}: ${event.event.value.message}`));
            this.grpc.close();
          }
          return;
        }
        this.handleEvent(event);
      };
      this.grpc.onClose = () => {
        clearTimeout(timer);
        if (!joined) reject(new Error("joinToolRoom: the relay closed the connection before admitting the page"));
        this.shutdown();
      };
      this.grpc.onError = () => {};
      this.grpc
        .connect(wsUrl(this.opts.relay, this.opts.room, this.opts.token))
        .then(() => {
          this.grpc.send({
            join: {
              sessionId: this.opts.room,
              username: this.opts.username ?? "page",
              clientType: "browser",
              protocolVersion: 2,
            },
          });
        })
        .catch((err: unknown) => {
          clearTimeout(timer);
          reject(err instanceof Error ? err : new Error(String(err)));
        });
    });
  }

  registerTool(tool: PageTool): { unregister(): void } {
    this.tools.set(tool.name, tool);
    if (this.modelContext) this.native.set(tool.name, this.modelContext.registerTool(tool));
    this.notifyBridges("notifications/tools/list_changed");
    return { unregister: () => this.unregisterTool(tool.name) };
  }

  unregisterTool(name: string): void {
    if (!this.tools.delete(name)) return;
    const handle = this.native.get(name) as { unregister?: () => void } | undefined;
    handle?.unregister?.();
    this.native.delete(name);
    this.notifyBridges("notifications/tools/list_changed");
  }

  setLabel(label: string): void {
    this.label = label;
    this.notifyBridges(PAGE_STATE, { label });
  }

  focus(): void {
    this.notifyBridges(PAGE_STATE, { label: this.label, focused: true });
  }

  setAgentsAllowed(allowed: boolean): void {
    this.allowed = allowed;
    this.notifyBridges(AGENTS_ALLOWED, { allowed });
  }

  acceptPairing(code: string): void {
    this.notifyBridges(ACCEPT_PAIRING, { code });
  }

  revoke(session = ""): void {
    this.notifyBridges(REVOKE, { session });
  }

  onPairingRequest(fn: (req: PairingRequest) => void): void {
    this.onPairing = fn;
  }

  leave(): void {
    if (this.left) return;
    try {
      this.grpc.send({ leave: { reason: "left" } });
    } catch {
      // The socket is already gone.
    }
    this.grpc.close();
    this.shutdown();
  }

  private shutdown(): void {
    if (this.left) return;
    this.left = true;
    for (const ctl of this.running.values()) ctl.abort();
    this.running.clear();
    this.resolveClosed();
  }

  private handleEvent(event: CollabEvent): void {
    switch (event.event.case) {
      case "peerLeft":
        this.bridges.delete(event.event.value.clientId);
        break;
      case "sessionEnded":
        this.grpc.close();
        this.shutdown();
        break;
      case "appMessage": {
        const msg = event.event.value;
        // Only messages addressed to this page; a room-wide one is someone
        // else's traffic.
        if (msg.kind !== MESSAGE_KIND || msg.toClientId !== this.id) return;
        let m: RpcMessage;
        try {
          m = JSON.parse(new TextDecoder().decode(msg.payload));
        } catch {
          return;
        }
        this.handle(event.fromClientId, m);
        break;
      }
    }
  }

  private handle(from: string, m: RpcMessage): void {
    switch (m.method) {
      case "tools/list":
        this.reply(from, m.id, { tools: this.descriptors() });
        this.greet(from);
        return;
      case "tools/call":
        this.greet(from);
        void this.call(from, m);
        return;
      case "notifications/cancelled":
        this.running.get(`${from}/${JSON.stringify(m.params?.requestId)}`)?.abort();
        return;
      case PAIRING_REQUESTED:
        this.onPairing?.(m.params as PairingRequest);
        return;
    }
    if (m.id !== undefined && m.method) {
      this.send(from, { id: m.id, error: { code: METHOD_NOT_FOUND, message: `method not found: ${m.method}` } });
    }
  }

  // A page cannot address a bridge before hearing from it, so the first time
  // one speaks it gets the state it missed.
  private greet(from: string): void {
    if (this.bridges.has(from)) return;
    this.bridges.add(from);
    if (this.label) this.send(from, { method: PAGE_STATE, params: { label: this.label } });
    if (this.allowed) this.send(from, { method: AGENTS_ALLOWED, params: { allowed: true } });
  }

  private descriptors() {
    return [...this.tools.keys()].sort().map((name) => {
      const t = this.tools.get(name)!;
      return {
        name: t.name,
        description: t.description ?? "",
        inputSchema: t.inputSchema ?? { type: "object" },
        annotations: { ...t.annotations, readOnlyHint: t.annotations?.readOnlyHint ?? false },
      };
    });
  }

  private async call(from: string, m: RpcMessage): Promise<void> {
    const name: string = m.params?.name;
    const tool = this.tools.get(name);
    if (!tool) {
      this.send(from, { id: m.id, error: { code: INVALID_PARAMS, message: `unknown tool: ${name}` } });
      return;
    }
    const key = `${from}/${JSON.stringify(m.id)}`;
    const ctl = new AbortController();
    this.running.set(key, ctl);
    const token = m.params?._meta?.progressToken;
    const progress = (done: number, total?: number, message?: string) => {
      if (token === undefined || ctl.signal.aborted) return;
      this.send(from, { method: "notifications/progress", params: { progressToken: token, progress: done, total, message } });
    };
    let result;
    try {
      result = toCallToolResult(await tool.execute(m.params?.arguments ?? {}, { signal: ctl.signal, progress, from }));
    } catch (err) {
      result = errorResult(err);
    } finally {
      this.running.delete(key);
    }
    // MCP sends no response for a cancelled request.
    if (ctl.signal.aborted) return;
    this.reply(from, m.id, result);
  }

  private notifyBridges(method: string, params?: unknown): void {
    for (const id of [...this.bridges].sort()) this.send(id, params === undefined ? { method } : { method, params });
  }

  private reply(to: string, id: RpcMessage["id"], result: unknown): void {
    this.send(to, { id, result });
  }

  private send(to: string, m: RpcMessage): void {
    if (this.left) return;
    const payload = new TextEncoder().encode(JSON.stringify({ jsonrpc: "2.0", ...m }));
    this.grpc.send({ appMessage: { kind: MESSAGE_KIND, payload: base64Encode(payload), toClientId: to } });
  }
}

function findModelContext(opt: JoinToolRoomOptions["modelContext"]): ModelContextLike | undefined {
  if (!opt) return undefined;
  if (opt !== true) return opt;
  const g = globalThis as any;
  return g.navigator?.modelContext ?? g.document?.modelContext ?? undefined;
}

/**
 * The relay's WebSocket URL for a room. The room is one path segment, so a
 * `/` in it is escaped.
 */
export function wsUrl(relay: string, room: string, token?: string): string {
  const u = new URL(relay);
  if (u.protocol === "http:") u.protocol = "ws:";
  else if (u.protocol === "https:") u.protocol = "wss:";
  else if (u.protocol !== "ws:" && u.protocol !== "wss:") {
    throw new Error(`joinToolRoom: relay url ${relay}: scheme must be http, https, ws or wss`);
  }
  const base = u.pathname.replace(/\/$/, "");
  u.pathname = `${base}/ws/v1/${encodeURIComponent(room)}/sync`;
  if (token) u.searchParams.set("token", token);
  return u.toString();
}
