// The Lanternfall page. It joins its game's room through the server's join
// endpoint, offers the game master's tools, and tells agents what the player
// does: resting on a place, walking, finding things, asking.
import { joinToolRoomFrom, type PageTool, type Presence, type ToolRoom } from "@panyam/mcpkit-rooms";
import catalog from "../../catalog.json";
import { neighbours, placeById, PLACES, type Place } from "./world.js";

const DWELL_MS = 600;
const SAME_PLACE_QUIET_MS = 3000;

interface LogEntry {
  who: "gm" | "you" | "world";
  text: string;
}

const state = {
  at: "gate",
  hidden: new Map<string, string[]>(PLACES.filter((p) => p.hidden).map((p) => [p.id, [p.hidden!]])),
  reveals: new Map<string, string[]>(),
  found: [] as string[],
  log: [] as LogEntry[],
};

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;

const FEED_MAX = 60;

/** The priority catalog.json gives an event type, for the activity feed. */
function priorityOf(name: string): string {
  const e = catalog.events.find((x) => x.name === name);
  return (e?.contextHint as { priority?: string } | undefined)?.priority ?? "built in";
}

function short(v: unknown): string {
  const s = JSON.stringify(v ?? {});
  return s.length > 90 ? s.slice(0, 87) + "…" : s;
}

/**
 * One line of the activity feed: what the page sends to agents (out), what
 * agents do to the page (in), and changes in who is around (info).
 */
function feed(dir: "out" | "in" | "info", text: string, tag = ""): void {
  const list = $("#feed");
  const li = document.createElement("li");
  li.className = dir;
  li.dataset.testid = `feed-${dir}`;
  const time = new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  const arrow = dir === "out" ? "→ event" : dir === "in" ? "← tool call" : "•";
  li.innerHTML = `<span class="t"></span> <span class="arrow"></span>${tag ? ' <span class="tag"></span>' : ""} <span class="what"></span>`;
  li.querySelector(".t")!.textContent = time;
  li.querySelector(".arrow")!.textContent = arrow;
  li.querySelector(".what")!.textContent = text;
  if (tag) {
    const t = li.querySelector(".tag")!;
    t.textContent = tag;
    t.classList.add(tag.replace(/\s+/g, "-"));
  }
  list.prepend(li);
  while (list.children.length > FEED_MAX) list.lastElementChild!.remove();
}

/** Emits a page event to the room and shows it in the feed with its declared priority. */
function emitEvent(name: string, data: Record<string, unknown>): void {
  room?.emit(name, data);
  feed("out", `${name} ${short(data)}`, priorityOf(name));
}

function say(who: LogEntry["who"], text: string): void {
  state.log.push({ who, text });
  const li = document.createElement("li");
  li.className = who;
  li.textContent = text;
  li.dataset.testid = `log-${who}`;
  $("#log").append(li);
  li.scrollIntoView({ block: "end" });
}

function render(): void {
  const map = $("#map");
  map.replaceChildren();
  const near = new Set(neighbours(state.at));
  for (const p of PLACES) {
    const tile = document.createElement("button");
    tile.className = "tile";
    tile.dataset.place = p.id;
    tile.dataset.testid = `tile-${p.id}`;
    if (p.id === state.at) tile.classList.add("here");
    if (near.has(p.id)) tile.classList.add("near");
    const reveals = state.reveals.get(p.id) ?? [];
    const action = p.id === state.at ? "🔍 click to search" : near.has(p.id) ? "👣 click to walk here" : "";
    tile.innerHTML = `<span class="icon">${p.icon}</span><span class="name"></span>${
      p.id === state.at ? '<span class="token" data-testid="token">🧭 you</span>' : ""
    }<ul class="reveals"></ul>${action ? `<span class="action">${action}</span>` : ""}`;
    tile.querySelector(".name")!.textContent = p.name;
    for (const r of reveals) {
      const li = document.createElement("li");
      li.textContent = r;
      tile.querySelector(".reveals")!.append(li);
    }
    tile.title = p.id === state.at ? "Search here" : near.has(p.id) ? `Walk to ${p.name}` : p.name;
    tile.addEventListener("click", () => onTileClick(p));
    tile.addEventListener("mouseenter", () => onDwellStart(p));
    tile.addEventListener("mouseleave", onDwellEnd);
    map.append(tile);
  }
  $("#inventory").textContent = state.found.length ? state.found.join(", ") : "nothing yet";
}

function lookAt(id: string) {
  const p = placeById(id);
  if (!p) throw new Error(`no place ${JSON.stringify(id)}; places are ${PLACES.map((x) => x.id).join(", ")}`);
  return {
    place: p.id,
    name: p.name,
    description: p.description,
    revealed: state.reveals.get(p.id) ?? [],
    neighbours: neighbours(p.id),
    playerIsHere: state.at === p.id,
    playerAt: state.at,
    playerCarries: state.found,
  };
}

function requirePlace(id: unknown): Place {
  const p = typeof id === "string" ? placeById(id) : undefined;
  if (!p) throw new Error(`no place ${JSON.stringify(id)}; places are ${PLACES.map((x) => x.id).join(", ")}`);
  return p;
}

const handlers: Record<string, PageTool["execute"]> = {
  look: ({ place }) => lookAt(typeof place === "string" && place ? place : state.at),
  move_player: ({ place }) => {
    const p = requirePlace(place);
    state.at = p.id;
    say("world", `The game master leads you to ${p.name}.`);
    render();
    return `moved to ${p.name}`;
  },
  reveal: ({ place, detail }) => {
    const p = requirePlace(place);
    const list = state.reveals.get(p.id) ?? [];
    list.push(String(detail));
    state.reveals.set(p.id, list);
    say("world", `Something new at ${p.name}: ${detail}`);
    render();
    return `revealed at ${p.name}`;
  },
  place_item: ({ place, item }) => {
    const p = requirePlace(place);
    const list = state.hidden.get(p.id) ?? [];
    list.push(String(item));
    state.hidden.set(p.id, list);
    return `hid ${item} at ${p.name}`;
  },
  narrate: ({ text }) => {
    say("gm", String(text));
    if ($("#status").textContent === "The game master is thinking…") $("#status").textContent = "";
    return "said";
  },
};

let room: ToolRoom | undefined;
let dwell: ReturnType<typeof setTimeout> | undefined;
let lastExamined = { place: "", at: 0 };

function onDwellStart(p: Place): void {
  onDwellEnd();
  dwell = setTimeout(() => {
    const now = Date.now();
    if (lastExamined.place === p.id && now - lastExamined.at < SAME_PLACE_QUIET_MS) return;
    lastExamined = { place: p.id, at: now };
    emitEvent("place.examined", { place: p.id, name: p.name });
  }, DWELL_MS);
}

function onDwellEnd(): void {
  if (dwell) clearTimeout(dwell);
  dwell = undefined;
}

function onTileClick(p: Place): void {
  if (p.id === state.at) {
    const items = state.hidden.get(p.id) ?? [];
    const item = items.shift();
    if (!item) {
      say("you", `You search ${p.name} and find nothing.`);
      return;
    }
    state.found.push(item);
    say("you", `You search ${p.name} and find ${item}.`);
    render();
    emitEvent("item.found", { item, place: p.id, name: p.name });
    return;
  }
  if (!neighbours(state.at).includes(p.id)) {
    say("you", `${p.name} is too far to walk to from here.`);
    return;
  }
  state.at = p.id;
  say("you", `You walk to ${p.name}.`);
  render();
  emitEvent("player.moved", { place: p.id, name: p.name });
}

let gamemasters: Presence[] = [];

function showPresence(agents: Presence[]): void {
  const key = (a: Presence) => `${a.client || "an agent"} for ${a.subject}`;
  const before = new Map(gamemasters.map((a) => [key(a), a]));
  for (const a of agents) {
    const was = before.get(key(a));
    if (!was) feed("info", `${key(a)} attached to this game`);
    else if (a.acting && !was.acting) feed("info", `${key(a)} started acting on this map`);
  }
  for (const k of before.keys()) if (!agents.some((a) => key(a) === k)) feed("info", `${k} left`);
  gamemasters = agents;
  $("#presence").textContent = agents.length
    ? agents.map((a) => `${a.client || "an agent"} for ${a.subject}${a.acting ? ", acting" : ", listening"}`).join(" · ")
    : "none connected yet";
  updateHint();
}

/** The banner under the header: what the player has to do next for a game master to play. */
function updateHint(): void {
  const hint = $("#hint");
  const allowed = ($("#gm-switch") as HTMLInputElement).checked;
  if (!gamemasters.length) {
    hint.hidden = false;
    hint.innerHTML =
      "<strong>No game master is connected yet.</strong> You can walk and search on your own, but nobody will answer. " +
      "To bring one in, run <code>make gm</code> in <code>examples/rooms/lanternfall</code> (needs <code>ANTHROPIC_API_KEY</code>), " +
      "or connect Claude Code as described under <em>Connect an agent</em> below.";
  } else if (!allowed) {
    hint.hidden = false;
    hint.innerHTML =
      "<strong>Your game master is here.</strong> Turn on <em>Let my AI game master see and change this map</em> so it can hear you and act on your map.";
  } else {
    hint.hidden = true;
  }
}

async function start(): Promise<void> {
  const params = new URLSearchParams(location.search);
  const game = params.get("game") || "demo";
  const who = await (await fetch("/whoami")).json();
  if (!who.player) {
    location.href = `/signin?as=${encodeURIComponent(prompt("Your name (lowercase letters, digits, dashes)") || "player")}&game=${encodeURIComponent(game)}`;
    return;
  }
  $("#player").textContent = who.player;
  $("#game").textContent = game;
  $("#mcp-token").textContent = `player:${who.player}`;
  $("#resource").textContent = `lanternfall:${game}`;
  render();
  say("world", "The lanterns of Lanternfall went out at a quarter past midnight. Nobody knows why.");

  room = await joinToolRoomFrom(`/rooms/join?game=${encodeURIComponent(game)}`);
  for (const t of catalog.tools) {
    room.registerTool({
      name: t.name,
      description: t.description,
      inputSchema: t.inputSchema,
      annotations: { readOnlyHint: t.readOnly },
      execute: async (args, ctx) => {
        const who = ctx?.caller ? ` for ${ctx.caller.subject}` : "";
        // resource, room and page route the call to this tab; they are the bridge's, not the tool's.
        const own = Object.fromEntries(Object.entries(args).filter(([k]) => !["resource", "room", "page"].includes(k)));
        feed("in", `${t.name}(${short(own)})${who}`, t.readOnly ? "read-only" : "acting");
        try {
          return await handlers[t.name](args, ctx);
        } catch (err) {
          feed("info", `${t.name} failed: ${(err as Error).message}`);
          throw err;
        }
      },
    });
  }
  for (const e of catalog.events) room.registerEvent(e);
  room.setLabel(`${who.player}'s map`);
  feed("info", `joined room lanternfall:${game} as ${who.player}; offering ${catalog.tools.length} tools, declaring ${catalog.events.length} events`);
  room.onPresence(showPresence);
  room.onToolRefused((r) => feed("info", `the bridge refused tool ${r.name}: ${r.reason}`));
  room.onEventRefused((r) => feed("info", `the bridge refused event ${r.name}: ${r.reason}`));
  room.onPairingRequest((req) => {
    $("#status").textContent = req.sameSubject
      ? `Your agent ${req.client ?? ""} wants to be game master. Turn on the switch.`
      : `${req.subject}'s agent wants to drive this tab. Accept its code to let it.`;
  });
  room.onPaired((p) => ($("#status").textContent = `${p.subject}'s agent may now drive this tab.`));
  room.closed.then(() => ($("#status").textContent = "Disconnected from the room. Reload to rejoin."));
  window.addEventListener("focus", () => room?.focus());
  room.focus();

  const sw = $("#gm-switch") as HTMLInputElement;
  sw.disabled = false;
  sw.addEventListener("change", () => {
    room?.setAgentsAllowed(sw.checked);
    feed("info", sw.checked ? "switch on: your agents may see events and call tools" : "switch off: your agents can no longer see or act");
    $("#status").textContent = sw.checked
      ? "Your game master may drive this tab. Walk around, search, and ask it anything below."
      : "Your game master can no longer drive this tab.";
    updateHint();
  });
  $("#ask-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const input = $("#ask") as HTMLInputElement;
    const text = input.value.trim();
    if (!text) return;
    room?.ask(text);
    feed("out", `room.ask ${short({ text })}`, "built in");
    say("you", `You ask: ${text}`);
    input.value = "";
    if (!gamemasters.length) {
      say("world", "(No game master is connected, so nobody heard that. See the note at the top to start one.)");
    } else if (!sw.checked) {
      say("world", "(Your game master can't hear you until you turn on the switch at the top.)");
    } else {
      $("#status").textContent = "The game master is thinking…";
    }
  });
  $("#code-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const input = $("#code") as HTMLInputElement;
    if (input.value.trim()) room?.acceptPairing(input.value.trim());
    input.value = "";
  });
  $("#status").textContent = `In the room as ${who.player}.`;
  updateHint();
  document.body.dataset.ready = "true";
  (window as any).lanternfall = { room, state };
}

interface CtxEntry {
  kind: string;
  text: string;
  tokens: number;
}

const KIND_LABEL: Record<string, string> = {
  question: "question",
  event: "event",
  summary: "summary",
  model: "model says",
  tool_call: "tool call",
  tool_result: "tool result",
};

/**
 * Follows the game master's context stream, which the LangGraph game master
 * (make gm) serves on localhost. Claude Code keeps its context to itself, so
 * with it the panel only says so.
 */
function followContext(): void {
  const port = new URLSearchParams(location.search).get("gm") || "8091";
  const url = `http://${location.hostname}:${port}/context`;
  const status = $("#ctx-status");
  const noStream = () => {
    status.textContent =
      `No context stream at ${url}. It appears when the LangGraph game master runs (make gm). ` +
      "Claude Code keeps its own context, so with Claude Code this panel stays empty.";
  };
  noStream();
  let seen = false;
  const es = new EventSource(url);
  es.onerror = () => {
    if (!seen) return noStream();
    status.textContent = "The game master's stream ended. Below is the last context it sent; it reconnects when the game master runs again.";
  };
  es.onmessage = (msg) => {
    seen = true;
    const s = JSON.parse(msg.data);
    status.textContent = `${s.model} for ${s.player}: ${s.status}. ${s.turns} turn${s.turns === 1 ? "" : "s"} so far.`;
    const pct = Math.min(100, Math.round((s.tokens / s.budget) * 100));
    const fill = $("#ctx-fill");
    fill.style.width = `${pct}%`;
    fill.classList.toggle("over", s.tokens > s.budget);
    const last = s.compactions[s.compactions.length - 1];
    $("#ctx-meta").textContent =
      `≈${s.tokens} of ${s.budget} tokens in ${s.entries.length} rows, plus a ${s.systemTokens}-token system prompt. ` +
      (last
        ? `Compacted ${s.compactions.length} time${s.compactions.length === 1 ? "" : "s"}; the last folded ${last.folded} messages (≈${last.tokensBefore} tokens) into the summary at the top.`
        : "Not compacted yet: it compacts when the conversation passes the budget.");
    $("#ctx-system-text").textContent = s.system;
    const list = $("#ctx");
    list.replaceChildren();
    if (s.compactions.length) {
      const li = document.createElement("li");
      li.className = "compaction";
      li.textContent = `⤓ ${last.folded} earlier messages were compacted into this summary`;
      list.append(li);
    }
    for (const e of s.entries as CtxEntry[]) {
      const li = document.createElement("li");
      li.className = e.kind;
      li.dataset.testid = `ctx-${e.kind}`;
      li.innerHTML = '<span class="kind"></span><span class="txt"></span><span class="tok"></span>';
      li.querySelector(".kind")!.textContent = KIND_LABEL[e.kind] ?? e.kind;
      li.querySelector(".txt")!.textContent = e.text;
      li.querySelector(".tok")!.textContent = `≈${e.tokens}`;
      li.title = "Click to show all of it";
      li.addEventListener("click", () => li.classList.toggle("open"));
      list.append(li);
    }
    list.scrollTop = list.scrollHeight;
  };
}

followContext();

start().catch((err) => {
  $("#status").textContent = `Could not join: ${err.message}`;
});
