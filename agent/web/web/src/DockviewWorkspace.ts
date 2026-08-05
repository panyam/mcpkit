import { DockviewComponent, themeDark, themeLight } from "dockview-core";
import type { DockviewApi } from "dockview-core";
import type { PanelId } from "./panels.js";
import type { ConversationStore } from "./conversation.js";
import { conversationIsland } from "./ConversationPanel.js";
import {
  DEFAULT_DOCK_LAYOUT,
  DOCK_LAYOUT_KEY,
  DOCK_PANELS,
  loadDockLayout,
  panelsToReconcile,
  resolveTheme,
  saveDockLayout,
} from "./dock.js";

const PANELS: PanelId[] = [...DOCK_PANELS];

// mountIsland mounts one panel's island into its detached host. Only the
// conversation panel exists this slice; #1198's panels extend this switch. The
// island renders into the host whether or not it is in the DOM yet, so building
// the islands before dockview adopts them is free.
function mountIsland(id: PanelId, host: HTMLElement, store: ConversationStore): void {
  switch (id) {
    case "conversation":
      conversationIsland(host, store);
      break;
  }
}

// DockviewWorkspace lays the panels out as draggable, resizable, dockable panes
// (dockview-core), the desktop variant. It builds one detached host per panel,
// mounts each island into it, then adopts the hosts into dock panels via
// dockview's createComponent factory, so the islands stay layout-agnostic. The
// rearranged layout persists in localStorage. dockview-core is loaded only on
// this path (a dynamic import in main.ts) so it never enters the mobile bundle.
// Ported from diffpp/web/workspace/DockviewWorkspace.ts, scoped to one panel.
export class DockviewWorkspace {
  private hosts: Partial<Record<PanelId, HTMLElement>> = {};
  private dockview!: DockviewApi;

  constructor(
    private readonly container: HTMLElement,
    private readonly store: ConversationStore,
  ) {}

  mount(): void {
    for (const id of PANELS) {
      const el = document.createElement("div");
      el.className = "ws-dock-panel";
      this.hosts[id] = el;
      mountIsland(id, el, this.store);
    }

    const dock = new DockviewComponent(this.container, {
      // component === PanelId (set in buildDefaultLayout / persisted layouts), so
      // the factory returns that panel's pre-built host div. An unknown name (a
      // stale saved layout naming a removed panel) yields an empty div rather
      // than crashing the restore.
      createComponent: (options) => {
        const el = this.hosts[options.name as PanelId] ?? document.createElement("div");
        return { element: el, init: () => {}, dispose: () => {} };
      },
      // "always" keeps a background tab's content element attached (hidden). The
      // default detaches it, which would strand the island rendered into that host.
      defaultRenderer: "always",
      theme: this.currentTheme(),
    });
    this.dockview = dock.api;

    const loaded = loadDockLayout(localStorage, DOCK_LAYOUT_KEY);
    if (loaded) {
      try {
        this.dockview.fromJSON(loaded.layout as Parameters<DockviewApi["fromJSON"]>[0]);
        this.reconcile(loaded.savedPanels);
      } catch {
        localStorage.removeItem(DOCK_LAYOUT_KEY);
        this.dockview.clear();
        this.buildDefaultLayout();
      }
    } else {
      this.buildDefaultLayout();
    }

    this.dockview.onDidLayoutChange(() => {
      saveDockLayout(localStorage, DOCK_LAYOUT_KEY, this.dockview.toJSON());
    });

    this.buildPanelsMenu();

    // Follow the shell's theme: a forced data-theme on <html>, else the OS scheme.
    const applyTheme = () => dock.updateOptions({ theme: this.currentTheme() });
    new MutationObserver(applyTheme).observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    window.matchMedia?.("(prefers-color-scheme: dark)").addEventListener?.("change", applyTheme);
  }

  private currentTheme() {
    const forced = document.documentElement.getAttribute("data-theme");
    const prefersDark = window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
    return resolveTheme(forced, prefersDark) === "dark" ? themeDark : themeLight;
  }

  private buildDefaultLayout(): void {
    for (const spec of DEFAULT_DOCK_LAYOUT) {
      this.dockview.addPanel({ id: spec.id, component: spec.id, title: spec.title, position: spec.position });
    }
  }

  // reconcile opens registry panels a later feature added since the layout was
  // saved: those absent from the saved registry and not already open. Panels the
  // user closed stay closed (they are in savedPanels).
  private reconcile(savedPanels: PanelId[]): void {
    for (const id of panelsToReconcile(savedPanels)) {
      if (this.dockview.getPanel(id)) continue;
      this.openPanel(id);
    }
  }

  // openPanel (re)opens a closed panel. The user drags it where they want and
  // persistence keeps it.
  private openPanel(id: PanelId): void {
    if (this.dockview.getPanel(id)) return;
    const spec = DEFAULT_DOCK_LAYOUT.find((s) => s.id === id);
    this.dockview.addPanel({ id, component: id, title: spec?.title ?? id, position: { direction: "right" } });
  }

  // buildPanelsMenu renders the "Panels" dropdown in the dock header: one toggle
  // per registry panel, a ✓ marking the open ones. It is the way back from a
  // tab-× close. Dock chrome, so it stays a plain DOM widget.
  private buildPanelsMenu(): void {
    const host = document.getElementById("dock-menu");
    if (!host) return;
    const api = this.dockview;
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "dock-menu-btn";
    btn.textContent = "Panels ▾";
    const list = document.createElement("div");
    list.className = "dock-menu-list";
    list.hidden = true;
    const rebuild = () => {
      list.replaceChildren();
      for (const id of PANELS) {
        const spec = DEFAULT_DOCK_LAYOUT.find((s) => s.id === id);
        const item = document.createElement("button");
        item.type = "button";
        item.className = "dock-menu-item";
        item.textContent = `${api.getPanel(id) ? "✓" : " "} ${spec?.title ?? id}`;
        item.addEventListener("click", () => {
          const panel = api.getPanel(id);
          if (panel) api.removePanel(panel);
          else this.openPanel(id);
          list.hidden = true;
        });
        list.appendChild(item);
      }
    };
    btn.addEventListener("click", () => {
      const show = list.hidden;
      if (show) rebuild();
      list.hidden = !show;
    });
    document.addEventListener("click", (e) => {
      if (!host.contains(e.target as Node)) list.hidden = true;
    });
    host.appendChild(btn);
    host.appendChild(list);
  }
}
