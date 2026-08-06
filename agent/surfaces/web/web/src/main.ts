import { hostClient } from "./api.js";
import { createPanelStores } from "./stores.js";
import { WatchStream } from "./watch.js";
import { mobileOverlaysIsland } from "./MobileOverlays.js";

// Bundle entry (esbuild -> static/app.js). The shell is server-rendered by
// serve.go with island holes inside #workspace; the server stamps the selected
// layout onto #workspace[data-layout]. This bootstrap builds the one shared
// ConversationStore, starts the Watch subscription that feeds it, then mounts
// the matching layout's islands. DockView is dynamically imported so
// dockview-core loads only on that layout, leaving the mobile bundle unaffected.

async function boot(rootEl: HTMLElement): Promise<void> {
  const client = hostClient();
  const stores = createPanelStores(client);

  // Live event stream: replay from offset 0 then live, decoded to HostEvents and
  // fanned to every panel projection. One subscription backs whichever layout is
  // mounted, so a turn streamed once lands in all open panels.
  const watch = new WatchStream(client, (ev) => stores.ingest(ev));
  watch.start();
  window.addEventListener("beforeunload", () => watch.stop());

  // Seed the status line (active model + session) once; SessionChanged frames
  // keep the session current after that.
  void client
    .getStatus({})
    .then((st) => {
      stores.conversation.setStatus({ model: st.modelLabel, session: st.runId });
      const line = document.getElementById("status-line");
      if (line) {
        const sess = st.runId ? ` · session ${st.runId}` : "";
        line.textContent = `model ${st.modelLabel || "—"}${sess}`;
      }
    })
    .catch(() => {});

  if (rootEl.dataset.layout === "mobile") {
    mobileOverlaysIsland(rootEl, stores);
    return;
  }

  const container = document.getElementById("dockview-container");
  if (!container) return;
  const { DockviewWorkspace } = await import("./DockviewWorkspace.js");
  new DockviewWorkspace(container, stores).mount();
}

// Wire the layout <select> (desktop dock vs mobile): switching reloads with the
// chosen ?layout=, which serve.go reads back into data-layout.
function wireLayoutSwitch(): void {
  const sel = document.getElementById("layout-select") as HTMLSelectElement | null;
  if (!sel) return;
  sel.addEventListener("change", () => {
    const u = new URL(location.href);
    u.searchParams.set("layout", sel.value);
    location.assign(u.toString());
  });
}

const rootEl = document.getElementById("workspace");
if (rootEl) {
  wireLayoutSwitch();
  void boot(rootEl);
}
