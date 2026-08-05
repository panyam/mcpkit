import { Show, createSignal } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { ConversationStore } from "./conversation.js";
import { Conversation } from "./ConversationPanel.js";

// MobileOverlays is the narrow-screen variant: instead of a dockable grid, the
// Conversation panel is presented as a slide-up overlay over a minimal home,
// the mobile-overlay pattern from diffpp/web/MobileOverlays.tsx. It is the SAME
// Conversation component the DockView panel embeds (one shared store), so a turn
// streamed once shows in whichever surface is mounted. A single panel today; as
// #1198 adds panels, the home grows a launcher tile per panel and each opens its
// own overlay.

function MobileHome(props: { store: ConversationStore }) {
  // The conversation overlay is open by default: it is the whole point of the
  // surface, and it makes the mobile mode land on the live turn immediately.
  const [open, setOpen] = createSignal(true);
  const s = props.store;
  const model = () => s.status().model || "agent";

  return (
    <div class="mobile-root">
      <div class="mobile-home">
        <div class="mobile-title">agentweb</div>
        <button type="button" class="mobile-launch" onClick={() => setOpen(true)}>
          <span class="mobile-launch-name">Conversation</span>
          <span class="mobile-launch-sub">{model()}</span>
        </button>
      </div>

      <Show when={open()}>
        <div class="mobile-overlay">
          <div class="mobile-overlay-bar">
            <span class="mobile-overlay-title">Conversation</span>
            <button type="button" class="mobile-overlay-close" aria-label="Close" onClick={() => setOpen(false)}>
              ✕
            </button>
          </div>
          <div class="mobile-overlay-body">
            <Conversation store={s} compact />
          </div>
        </div>
      </Show>
    </div>
  );
}

// mobileOverlaysIsland mounts the mobile surface into el as a tsappkit-solid
// SolidIsland and returns it so the caller can dispose it.
export function mobileOverlaysIsland(el: HTMLElement, store: ConversationStore): SolidIsland {
  const island = new SolidIsland("mobile-overlays", el, () => <MobileHome store={store} />, null);
  island.activate();
  return island;
}
