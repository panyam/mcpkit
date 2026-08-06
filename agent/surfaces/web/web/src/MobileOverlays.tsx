import { For, Show, createSignal, type JSX } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { PanelStores } from "./stores.js";
import { Conversation } from "./ConversationPanel.js";
import { SubAgentTree } from "./SubAgentPanel.js";
import { Timeline } from "./TimelinePanel.js";
import { Memory } from "./MemoryPanel.js";
import { Tools } from "./ToolsPanel.js";
import { Budget } from "./BudgetPanel.js";

// MobileOverlays is the narrow-screen variant: instead of a dockable grid, each
// panel is a slide-up overlay over a minimal home, the mobile-overlay pattern
// from diffpp/web/MobileOverlays.tsx. It embeds the SAME panel components the
// DockView surface does (one shared PanelStores bundle), so a turn streamed once
// shows in whichever surface is mounted. #1197 shipped one tile (Conversation);
// #1198 grows the home a launcher tile per observability panel.

interface Tile {
  key: string;
  name: string;
  sub: string;
  body: (s: PanelStores) => JSX.Element;
}

function MobileHome(props: { store: PanelStores }) {
  const s = props.store;
  const model = () => s.conversation.status().model || "agent";

  const tiles: Tile[] = [
    { key: "conversation", name: "Conversation", sub: model(), body: (b) => <Conversation store={b.conversation} compact /> },
    { key: "subagents", name: "Sub-agents", sub: "nested agent activity", body: (b) => <SubAgentTree store={b.subAgents} /> },
    { key: "timeline", name: "Activity", sub: "event timeline", body: (b) => <Timeline store={b.timeline} /> },
    { key: "tools", name: "Tools & Offload", sub: "tool calls", body: (b) => <Tools store={b.tools} /> },
    { key: "memory", name: "Memory", sub: "working memory", body: (b) => <Memory store={b.memory} /> },
    { key: "budget", name: "Budget", sub: "tokens & steps", body: (b) => <Budget store={b.budget} /> },
  ];

  // Conversation is open by default: it is the whole point of the surface and
  // lands the mobile mode on the live turn immediately.
  const [open, setOpen] = createSignal<string | null>("conversation");
  const active = () => tiles.find((t) => t.key === open());

  return (
    <div class="mobile-root">
      <div class="mobile-home">
        <div class="mobile-title">agentweb</div>
        <div class="mobile-tiles">
          <For each={tiles}>
            {(t) => (
              <button type="button" class="mobile-launch" onClick={() => setOpen(t.key)}>
                <span class="mobile-launch-name">{t.name}</span>
                <span class="mobile-launch-sub">{t.sub}</span>
              </button>
            )}
          </For>
        </div>
      </div>

      <Show when={active()}>
        {(t) => (
          <div class="mobile-overlay">
            <div class="mobile-overlay-bar">
              <span class="mobile-overlay-title">{t().name}</span>
              <button type="button" class="mobile-overlay-close" aria-label="Close" onClick={() => setOpen(null)}>
                ✕
              </button>
            </div>
            <div class="mobile-overlay-body">{t().body(s)}</div>
          </div>
        )}
      </Show>
    </div>
  );
}

// mobileOverlaysIsland mounts the mobile surface into el as a tsappkit-solid
// SolidIsland and returns it so the caller can dispose it.
export function mobileOverlaysIsland(el: HTMLElement, store: PanelStores): SolidIsland {
  const island = new SolidIsland("mobile-overlays", el, () => <MobileHome store={store} />, null);
  island.activate();
  return island;
}
