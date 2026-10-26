import { CollabClient } from "@panyam/massrelay";
import { GRPCWSClient } from "@panyam/servicekit-client";
import { DEFAULT_SEND_RATE, Sender } from "./sender.js";
import { errorResult, toCallToolResult, type PageTool } from "./tool.js";

/** The AppMessage kind that carries MCP JSON-RPC between a page and the bridge. */
export const MESSAGE_KIND = "mcp/1";

// Methods beyond MCP's own, mirroring experimental/ext/rooms/wire.go.
const PAGE_STATE = "notifications/rooms/page_state";
const AGENTS_ALLOWED = "notifications/rooms/agents_allowed";
const ACCEPT_PAIRING = "notifications/rooms/accept_pairing";
const GRANTS = "notifications/rooms/grants";
// The _meta key holding the caller's principal on a call, matching rooms.MetaCaller.
const CALLER_META = "io.mcpkit/rooms/caller";
const PAIRING_REQUESTED = "notifications/rooms/pairing_requested";
const PAGE_EVENT = "notifications/rooms/event";
const PAIRING_OFFER = "notifications/rooms/pairing_offer";
const PAIRED_BY_CODE = "notifications/rooms/paired_by_code";
const PRESENCE = "notifications/rooms/presence";
const NOTIFY = "notifications/rooms/notify";
const TOOL_REFUSED = "notifications/rooms/tool_refused";

/** How long an agent's attach code stays acceptable, matching `rooms.DefaultAgentCodeTTL`. */
const AGENT_CODE_TTL_MS = 10 * 60 * 1000;
/** How long an offered pairing code stays valid by default, matching `rooms.DefaultPairingCodeTTL`. */
export const DEFAULT_PAIRING_CODE_TTL_MS = 2 * 60 * 1000;
// Leaves out 0, O, 1 and I, which a user reading a code aloud mixes up.
const PAIRING_ALPHABET = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789";

/** How long a page keeps an agent in `presence()` after a bridge last reported it, matching `rooms.DefaultPresenceTTL`. */
export const DEFAULT_PRESENCE_TTL_MS = 60 * 60 * 1000;

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

/**
 * Who a call acts for: the subject of the caller's validated token, and the
 * client it used. A grant with no client covers every client of its subject.
 */
export interface Principal {
  subject: string;
  client?: string;
}

/** An agent asking to drive the page, as the bridge describes it. */
export interface PairingRequest {
  /** The agent's subject, from its validated token. */
  subject: string;
  /** The agent client that asked, when it said. */
  client?: string;
  /** True when it is the page's own user, so the agents switch is enough. */
  sameSubject: boolean;
}

/** A session that paired with the page by entering the code it offered. */
export interface PairedByCode extends PairingRequest {
  /** The code it entered, now spent. */
  code: string;
}

/**
 * An agent a bridge has seen around the page: one that called it (`acting`),
 * or attached to its room without calling yet. It names a principal, never a
 * session.
 */
/** A page tool a bridge will not offer, with the bridge's reason. */
export interface ToolRefusal {
  name: string;
  reason: string;
}

export interface Presence extends Principal {
  /** When a bridge last saw it, never later than the page's own clock. */
  lastSeen: Date;
  /** True once any bridge has seen it call the page. */
  acting: boolean;
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
  /**
   * How long `presence()` keeps an agent after a bridge last reported seeing
   * it. Default `DEFAULT_PRESENCE_TTL_MS`.
   */
  presenceTtlMs?: number;
  /**
   * The relay subjects of the host's bridges. When set, the page answers only
   * peers the relay validated a token for with one of these subjects, the way
   * `rooms.TrustBridges` does for a Go page. Without it, any holder of a relay
   * token for the room can call the page's tools directly, past the bridge's
   * agreement check, and `ctx.caller` says whatever that peer claims.
   */
  trustBridges?: string[];
  /**
   * The messages a second the page sends at most, so it stays under the
   * relay's per-connection limit (30 a second by default). Default
   * `DEFAULT_SEND_RATE`; 0 turns pacing off. Either way, a message the relay
   * drops for the rate is sent again.
   */
  sendRate?: number;
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
  /**
   * Accepts the code an agent got back from `attach`. The bridge answers with
   * the agent's principal, which the page then agrees to (`onPaired` reports
   * it) and sends to every bridge with its other grants.
   */
  acceptPairing(code: string): void;
  /**
   * Ends the page's agreement with `who`: every grant to its subject when no
   * client is given, or the one for that client. The agents switch is
   * unchanged.
   */
  revoke(who: Principal): void;
  /** Ends every grant. The agents switch is unchanged. */
  revokeAll(): void;
  /** The principals the page agrees to beyond its agents switch. */
  grants(): Principal[];
  /** Called when a session asks to drive the page. */
  onPairingRequest(fn: (req: PairingRequest) => void): void;
  /**
   * Makes up a code for the page's user to give an agent, which enters it
   * with the bridge's `pair` tool, and offers it to the bridges. Replaces any
   * code offered before. Single-use: once a session pairs with it the page
   * withdraws it and `onPaired` runs. Bridges honour at most ten minutes.
   */
  offerPairingCode(opts?: { ttlMs?: number }): string;
  /** Withdraws the code `offerPairingCode` made, if any. */
  withdrawPairingCode(): void;
  /** Called when a session pairs by the offered code: where the page shows its user which agent it was. */
  onPaired(fn: (paired: PairedByCode) => void): void;
  /**
   * Called whenever a bridge reports the agents it has seen around the page,
   * with the merged list `presence()` returns. Where a page shows its user
   * which agents have used the tab, and offers to revoke them.
   */
  onPresence(fn: (agents: Presence[]) => void): void;
  /**
   * The agents seen around the page within the presence TTL, merged across
   * every bridge in the room: one entry per principal, with the latest
   * `lastSeen`, and `acting` if any bridge saw it call. Sorted by subject,
   * then client.
   */
  presence(): Presence[];
  /**
   * Called when a bridge refuses one of the page's tools, with the bridge's
   * reason: a name the bridge reserves, one its host did not declare, or a
   * schema or read-only hint that differs from the one the bridge already
   * offers. Agents never see a refused tool, so this is where a page tells
   * its user that tool will not be called. A bridge refuses again each time
   * it relists the page's tools; the page reports a name once per
   * registration, and again only if the reason changes.
   */
  onToolRefused(fn: (refused: ToolRefusal) => void): void;
  /**
   * The refusals the page has heard for tools it still registers, by name,
   * so a page that installs `onToolRefused` late can catch up. Registering
   * or unregistering the name again clears its entry.
   */
  refusedTools(): ToolRefusal[];
  /**
   * Installs the handler for the host's notifications named `name`, which the
   * host sends its pages with the bridge's `Notify` (no agent involved),
   * replacing any before it. `data` is what the host sent, `null` when it
   * sent none. A notification with no handler is dropped. Any peer in the
   * room can send one, as with `ctx.caller`, so treat it as a prompt to
   * refresh rather than as the change itself.
   */
  onNotify(name: string, fn: (data: unknown) => void): { off(): void };
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
  private onPairedFn?: (paired: PairedByCode) => void;
  private offer?: { code: string; expires: number };
  private grantList: Principal[] = [];
  // Agent codes the page's user accepted, until a bridge names the principal.
  private accepted = new Map<string, number>();
  private focusedAt?: string;
  // Merged agent presence, by subject and client.
  private seen = new Map<string, Presence>();
  private onPresenceFn?: (agents: Presence[]) => void;
  private refused = new Map<string, ToolRefusal>();
  private onToolRefusedFn?: (refused: ToolRefusal) => void;
  private notifyHandlers = new Map<string, (data: unknown) => void>();
  private left = false;
  private modelContext?: ModelContextLike;

  private client!: CollabClient;
  private sender: Sender;
  private trusted?: Set<string>;

  constructor(private opts: JoinToolRoomOptions) {
    this.room = opts.room;
    this.closed = new Promise((resolve) => (this.resolveClosed = resolve));
    this.modelContext = findModelContext(opts.modelContext);
    this.sender = new Sender((action) => this.client.send(action), opts.sendRate ?? DEFAULT_SEND_RATE);
    if (opts.trustBridges) this.trusted = new Set(opts.trustBridges);
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
        // knows, so a dropped or evicted page ends and `closed` resolves
        // instead.
        reconnect: false,
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
        // join it refuses one message and the page stays in the room:
        // PEER_NOT_FOUND for a reply to a bridge that has just left, or
        // RATE_LIMITED for one the sender sends again.
        onErrorEvent: (code, message) => {
          if (!joined) refuse(new Error(`joinToolRoom: ${code}: ${message}`));
          else if (code === "RATE_LIMITED") this.sender.rateLimited(message);
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
          if (this.trusted && !this.trusted.has(this.client.getPeer(from)?.identity?.subject ?? "")) return;
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
    this.refused.delete(tool.name);
    if (this.modelContext) this.native.set(tool.name, this.modelContext.registerTool(tool));
    this.notifyBridges("notifications/tools/list_changed");
    return { unregister: () => this.unregisterTool(tool.name) };
  }

  unregisterTool(name: string): void {
    if (!this.tools.delete(name)) return;
    this.refused.delete(name);
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
    this.focusedAt = new Date().toISOString();
    this.notifyBridges(PAGE_STATE, { label: this.label, focused: true, focusedAt: this.focusedAt });
  }

  setAgentsAllowed(allowed: boolean): void {
    this.allowed = allowed;
    this.notifyBridges(AGENTS_ALLOWED, { allowed });
  }

  acceptPairing(code: string): void {
    code = code.trim().toUpperCase();
    this.accepted.set(code, Date.now() + AGENT_CODE_TTL_MS);
    this.notifyBridges(ACCEPT_PAIRING, { code });
  }

  revoke(who: Principal): void {
    this.grantList = this.grantList.filter((g) => !(g.subject === who.subject && (!who.client || g.client === who.client)));
    this.sendGrants();
  }

  revokeAll(): void {
    this.grantList = [];
    this.sendGrants();
  }

  grants(): Principal[] {
    return this.grantList.map((g) => ({ ...g }));
  }

  private sendGrants(): void {
    this.notifyBridges(GRANTS, { grants: this.grantList });
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

  offerPairingCode(opts: { ttlMs?: number } = {}): string {
    const ttlMs = opts.ttlMs && opts.ttlMs > 0 ? opts.ttlMs : DEFAULT_PAIRING_CODE_TTL_MS;
    const code = newPairingCode();
    this.offer = { code, expires: Date.now() + ttlMs };
    this.notifyBridges(PAIRING_OFFER, { code, ttlMs });
    return code;
  }

  withdrawPairingCode(): void {
    this.offer = undefined;
    this.notifyBridges(PAIRING_OFFER, { code: "" });
  }

  onPaired(fn: (paired: PairedByCode) => void): void {
    this.onPairedFn = fn;
  }

  onPresence(fn: (agents: Presence[]) => void): void {
    this.onPresenceFn = fn;
  }

  onToolRefused(fn: (refused: ToolRefusal) => void): void {
    this.onToolRefusedFn = fn;
  }

  refusedTools(): ToolRefusal[] {
    return [...this.refused.values()];
  }

  onNotify(name: string, fn: (data: unknown) => void): { off(): void } {
    this.notifyHandlers.set(name, fn);
    return {
      off: () => {
        if (this.notifyHandlers.get(name) === fn) this.notifyHandlers.delete(name);
      },
    };
  }

  presence(): Presence[] {
    const ttl = this.opts.presenceTtlMs ?? DEFAULT_PRESENCE_TTL_MS;
    const now = Date.now();
    for (const [k, e] of this.seen) if (now - e.lastSeen.getTime() > ttl) this.seen.delete(k);
    return [...this.seen.values()]
      .map((e) => ({ ...e }))
      .sort((a, b) => cmp(a.subject, b.subject) || cmp(a.client ?? "", b.client ?? ""));
  }

  // Folds one bridge's report into the merged list. A bridge's clock is not
  // trusted to be ahead of the page's, so a future lastSeen counts as now.
  private mergePresence(params: any): void {
    const now = Date.now();
    for (const e of Array.isArray(params?.agents) ? params.agents : []) {
      if (!e?.subject) continue;
      const at = Math.min(Date.parse(e.lastSeen) || now, now);
      const key = `${e.subject}\u0000${e.client ?? ""}`;
      const cur = this.seen.get(key);
      const entry: Presence = { subject: e.subject, lastSeen: new Date(Math.max(at, cur?.lastSeen.getTime() ?? 0)), acting: !!e.acting || !!cur?.acting };
      if (e.client) entry.client = e.client;
      this.seen.set(key, entry);
    }
    this.onPresenceFn?.(this.presence());
  }

  leave(): void {
    if (this.left) return;
    this.client.disconnect();
    this.shutdown();
  }

  private shutdown(): void {
    if (this.left) return;
    this.left = true;
    this.sender.stop();
    for (const ctl of this.running.values()) ctl.abort();
    this.running.clear();
    this.resolveClosed();
  }

  private handle(from: string, m: RpcMessage): void {
    switch (m.method) {
      case "tools/list":
        // Greet before answering: a bridge handles one peer's messages in
        // order, so by the time it has the list it also has the page's state.
        this.greet(from);
        this.reply(from, m.id, { tools: this.descriptors() });
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
      case PRESENCE:
        this.mergePresence(m.params);
        return;
      case TOOL_REFUSED: {
        const name = m.params?.name;
        if (typeof name !== "string" || !this.tools.has(name)) return;
        const refusal = { name, reason: typeof m.params?.reason === "string" ? m.params.reason : "" };
        if (this.refused.get(name)?.reason === refusal.reason) return;
        this.refused.set(name, refusal);
        this.onToolRefusedFn?.(refusal);
        return;
      }
      case NOTIFY:
        if (typeof m.params?.name === "string") this.notifyHandlers.get(m.params.name)?.(m.params.data ?? null);
        return;
      case PAIRED_BY_CODE: {
        // Only a code this page offered, or an agent's code its user
        // accepted, counts; any other claim grants nothing, so a peer that
        // is not a bridge cannot grant itself access.
        const paired = m.params as PairedByCode;
        const code = (paired?.code ?? "").toUpperCase();
        const now = Date.now();
        const offered = !!code && code === this.offer?.code && now < this.offer.expires;
        const acceptedUntil = this.accepted.get(code);
        if ((!offered && !(acceptedUntil && now < acceptedUntil)) || !paired.subject) return;
        this.accepted.delete(code);
        if (!this.grantList.some((g) => g.subject === paired.subject && (g.client ?? "") === (paired.client ?? ""))) {
          this.grantList.push(paired.client ? { subject: paired.subject, client: paired.client } : { subject: paired.subject });
        }
        if (offered) this.withdrawPairingCode();
        this.sendGrants();
        this.onPairedFn?.(paired);
        return;
      }
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
    if (this.focusedAt) this.send(from, { method: PAGE_STATE, params: { label: this.label, focused: true, focusedAt: this.focusedAt } });
    else if (this.label) this.send(from, { method: PAGE_STATE, params: { label: this.label } });
    if (this.allowed) this.send(from, { method: AGENTS_ALLOWED, params: { allowed: true } });
    if (this.grantList.length) this.send(from, { method: GRANTS, params: { grants: this.grantList } });
    const left = this.offer ? this.offer.expires - Date.now() : 0;
    if (this.offer && left > 0) this.send(from, { method: PAIRING_OFFER, params: { code: this.offer.code, ttlMs: left } });
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
      const caller = m.params?._meta?.[CALLER_META];
      result = toCallToolResult(await tool.execute(m.params?.arguments ?? {}, { signal: ctl.signal, progress, from, caller }));
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
    this.sender.send(MESSAGE_KIND, new TextEncoder().encode(JSON.stringify({ jsonrpc: "2.0", ...m })), to);
  }
}

function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function newPairingCode(): string {
  const bytes = new Uint8Array(8);
  globalThis.crypto.getRandomValues(bytes);
  // 256 is a multiple of the 32-letter alphabet, so this is unbiased.
  return [...bytes].map((b) => PAIRING_ALPHABET[b % PAIRING_ALPHABET.length]).join("");
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
