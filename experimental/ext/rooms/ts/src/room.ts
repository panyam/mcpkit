import { CollabClient } from "@panyam/massrelay";
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
const PAGE_EVENT = "notifications/rooms/event";

/** Event name `ask()` emits: the page's user asked the agents something. */
export const EVENT_ASK = "room.ask";
/** Event name `selectionChanged()` emits. */
export const EVENT_SELECTION_CHANGED = "page.selection_changed";
/** Largest encoded `data` one event may carry, matching the bridge's cap. */
export const MAX_EVENT_DATA_BYTES = 16 << 10;
const EVENT_NAME = /^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$/;

/**
 * Reports whether `name` is one a page may emit: one to four dot-separated
 * lowercase segments, each starting with a letter, at most 64 characters.
 * Mirrors `rooms.ValidEventName`.
 */
export function validEventName(name: string): boolean {
  return name.length <= 64 && EVENT_NAME.test(name);
}
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
  /**
   * Tells the bridges something happened on the page. Agents subscribed to
   * the room's events hear it, if this page agreed to their session. Throws
   * on a name `validEventName` refuses or data over `MAX_EVENT_DATA_BYTES`.
   * Bridges hear only once they have greeted the page, so an event emitted
   * before any bridge listed its tools goes nowhere.
   */
  emit(name: string, data?: unknown): void;
  /** Emits `room.ask`: the page's user asked the agents `text`. */
  ask(text: string): void;
  /** Emits `page.selection_changed` with whatever the page counts as its selection. */
  selectionChanged(selection: unknown): void;
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
  const page = new Page(opts);
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

  private client!: CollabClient;

  constructor(private opts: JoinToolRoomOptions) {
    this.room = opts.room;
    this.closed = new Promise((resolve) => (this.resolveClosed = resolve));
    this.modelContext = findModelContext(opts.modelContext);
  }

  join(): Promise<void> {
    return new Promise((resolve, reject) => {
      let joined = false;
      const refuse = (err: Error) => {
        clearTimeout(timer);
        reject(err);
      };
      const timer = setTimeout(() => {
        if (joined) return;
        reject(new Error(`joinToolRoom: relay did not admit the page within ${this.opts.joinTimeoutMs ?? 10000}ms`));
        this.client.disconnect();
      }, this.opts.joinTimeoutMs ?? 10000);
      const WebSocketImpl = this.opts.WebSocket;
      this.client = new CollabClient({
        token: this.opts.token,
        // A reconnect would rejoin under a new peer id, which no pairing
        // knows, so a dropped page ends and `closed` resolves instead.
        maxRetries: 0,
        // CollabClient takes a WebSocket implementation only through the
        // GRPCWSClient it builds on.
        _grpcFactory: WebSocketImpl ? () => new GRPCWSClient({ WebSocket: WebSocketImpl }) : undefined,
        onConnect: (clientId) => {
          joined = true;
          clearTimeout(timer);
          this.id = clientId;
          resolve();
        },
        // Before the join an ErrorEvent is the relay refusing it. After the
        // join it refuses one message, typically PEER_NOT_FOUND for a reply
        // to a bridge that has just left, and the page stays in the room.
        onErrorEvent: (code, message) => {
          if (!joined) refuse(new Error(`joinToolRoom: ${code}: ${message}`));
        },
        onError: (err) => {
          if (!joined) refuse(err);
        },
        onDisconnect: () => this.shutdown(),
        onPeerLeft: (clientId) => this.bridges.delete(clientId),
        onAppMessage: (kind, payload, from, to) => {
          // Only messages addressed to this page; a room-wide one is someone
          // else's traffic.
          if (kind !== MESSAGE_KIND || to !== this.id) return;
          let m: RpcMessage;
          try {
            m = JSON.parse(new TextDecoder().decode(payload));
          } catch {
            return;
          }
          this.handle(from, m);
        },
      });
      try {
        this.client.connect(relayBase(this.opts.relay), this.opts.room, this.opts.username ?? "page", {});
      } catch (err) {
        refuse(err instanceof Error ? err : new Error(String(err)));
      }
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

  emit(name: string, data: unknown = null): void {
    if (!validEventName(name)) throw new Error(`invalid page event name ${JSON.stringify(name)}`);
    const encoded = JSON.stringify(data ?? null);
    const size = new TextEncoder().encode(encoded).length;
    if (size > MAX_EVENT_DATA_BYTES) {
      throw new Error(`page event data is ${size} bytes, over ${MAX_EVENT_DATA_BYTES}`);
    }
    this.notifyBridges(PAGE_EVENT, { name, data: data ?? null });
  }

  ask(text: string): void {
    this.emit(EVENT_ASK, { text });
  }

  selectionChanged(selection: unknown): void {
    this.emit(EVENT_SELECTION_CHANGED, { selection });
  }

  onPairingRequest(fn: (req: PairingRequest) => void): void {
    this.onPairing = fn;
  }

  leave(): void {
    if (this.left) return;
    this.client.disconnect();
    this.shutdown();
  }

  private shutdown(): void {
    if (this.left) return;
    this.left = true;
    for (const ctl of this.running.values()) ctl.abort();
    this.running.clear();
    this.resolveClosed();
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
    this.client.sendAppMessage(MESSAGE_KIND, payload, { to });
  }
}

function findModelContext(opt: JoinToolRoomOptions["modelContext"]): ModelContextLike | undefined {
  if (!opt) return undefined;
  if (opt !== true) return opt;
  const g = globalThis as any;
  return g.navigator?.modelContext ?? g.document?.modelContext ?? undefined;
}

/**
 * The base URL CollabClient appends its room path to: ws(s), with any mount
 * path, and no trailing slash or query.
 */
function relayBase(relay: string): string {
  const u = new URL(wsUrl(relay, "_"));
  u.pathname = u.pathname.replace(/\/ws\/v1\/_\/sync$/, "");
  u.search = "";
  return u.toString().replace(/\/$/, "");
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
