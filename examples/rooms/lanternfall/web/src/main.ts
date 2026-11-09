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
    room?.emit("place.examined", { place: p.id, name: p.name });
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
    room?.emit("item.found", { item, place: p.id, name: p.name });
    return;
  }
  if (!neighbours(state.at).includes(p.id)) {
    say("you", `${p.name} is too far to walk to from here.`);
    return;
  }
  state.at = p.id;
  say("you", `You walk to ${p.name}.`);
  render();
  room?.emit("player.moved", { place: p.id, name: p.name });
}

let gamemasters: Presence[] = [];

function showPresence(agents: Presence[]): void {
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
      execute: handlers[t.name],
    });
  }
  for (const e of catalog.events) room.registerEvent(e);
  room.setLabel(`${who.player}'s map`);
  room.onPresence(showPresence);
  room.onToolRefused((r) => say("world", `(the bridge refused tool ${r.name}: ${r.reason})`));
  room.onEventRefused((r) => say("world", `(the bridge refused event ${r.name}: ${r.reason})`));
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

start().catch((err) => {
  $("#status").textContent = `Could not join: ${err.message}`;
});
