import type { PanelId } from "./panels.js";

// dock.ts is the framework-neutral core of the DockView layout: the default
// panel arrangement, layout persistence, and theme resolution — all DOM-free so
// they unit-test without dockview or a browser. DockviewWorkspace.ts is the thin
// glue that feeds these to dockview-core. Ported from diffpp/web/workspace/dock.ts,
// scoped to this surface's registry (issue 1197).

// DockPanelSpec is one dockview addPanel call expressed as data. position mirrors
// dockview's: a direction relative to an already-added referencePanel; an absent
// position anchors the panel at the grid root.
export interface DockPanelSpec {
  id: PanelId;
  title: string;
  position?: {
    direction: "left" | "right" | "above" | "below" | "within";
    referencePanel?: PanelId;
  };
  // initialWidth/initialHeight seed the split proportions for the first-run
  // default layout so the panes open at sensible sizes (dockview's own default
  // split leaves the reference panel too narrow). Users resize from there and
  // the arrangement persists.
  initialWidth?: number;
  initialHeight?: number;
}

// DEFAULT_DOCK_LAYOUT is the first-run arrangement, applied only when no saved
// layout exists. This slice ships one panel; #1198's panels are added here (each
// positioned relative to conversation) and appear for existing users via reconcile.
export const DEFAULT_DOCK_LAYOUT: DockPanelSpec[] = [
  { id: "conversation", title: "Conversation" },
  { id: "subagents", title: "Sub-agents", position: { direction: "right", referencePanel: "conversation" }, initialWidth: 1000 },
  { id: "timeline", title: "Activity", position: { direction: "below", referencePanel: "subagents" }, initialHeight: 430 },
  { id: "tools", title: "Tools & Offload", position: { direction: "within", referencePanel: "timeline" } },
  { id: "memory", title: "Memory", position: { direction: "within", referencePanel: "timeline" } },
  { id: "budget", title: "Budget", position: { direction: "below", referencePanel: "conversation" }, initialHeight: 470 },
];

// DOCK_LAYOUT_KEY is the localStorage key the rearranged dockview layout persists
// under, so a reload restores the user's arrangement.
export const DOCK_LAYOUT_KEY = "agentweb-dock-layout";

// DOCK_PANELS is the panel registry the dock is built from, in a stable order.
// Persisted with each saved layout as its "version marker" (see SavedLayout): a
// self-updating one, derived from the registry rather than a hand-bumped integer.
// Every id here shows in the Panels menu and counts toward reconcile.
export const DOCK_PANELS: readonly PanelId[] = ["conversation", "subagents", "timeline", "tools", "memory", "budget"];

// RECONCILE_AUTO_OPEN is the subset a reconcile pass OPENS for an existing saved
// layout when the panel is new since the save. The observability panels (#1198)
// are deliberately NOT here: they ship in the menu and in a fresh default
// layout, but a user who already arranged their workspace under #1197 keeps it
// as-is — the new panels appear in the Panels menu without popping open over
// their arrangement. A fresh user (no saved layout) still gets the full
// DEFAULT_DOCK_LAYOUT.
export const RECONCILE_AUTO_OPEN: readonly PanelId[] = ["conversation"];

// DockStorage is the minimal Web Storage subset dock persistence needs, so tests
// inject a fake without a DOM. localStorage satisfies it directly.
export interface DockStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

// SavedLayout is the persisted shape: a schema version, the panel registry that
// existed at save time, and the opaque dockview toJSON() blob. panels is what
// lets restore tell a NEWLY ADDED panel (in DOCK_PANELS, absent from panels)
// apart from a USER-CLOSED one (present in panels, absent from the layout).
// Without it a saved layout would freeze the panel set, so a panel a later
// feature (#1198) adds would never appear for anyone with a saved arrangement.
interface SavedLayout {
  v: 1;
  panels: PanelId[];
  layout: unknown;
}

// LoadedLayout is what loadDockLayout hands the caller: the dockview blob plus the
// registry at save time. savedPanels is empty for a pre-versioning bare save,
// which makes every current panel count as "new" for one reconcile pass, after
// which the next save rewrites it as a versioned wrapper.
export interface LoadedLayout {
  layout: unknown;
  savedPanels: PanelId[];
}

// saveDockLayout persists the dockview blob stamped with the current registry. A
// storage failure (quota, private mode) must not take the workspace down, so it
// is swallowed. The caller passes whatever dockview.toJSON() returns.
export function saveDockLayout(storage: DockStorage, key: string, layout: unknown): void {
  try {
    const wrapped: SavedLayout = { v: 1, panels: [...DOCK_PANELS], layout };
    storage.setItem(key, JSON.stringify(wrapped));
  } catch {
    // best-effort persistence
  }
}

// loadDockLayout returns the saved layout + its registry, or null when it is
// absent or corrupt (the caller falls back to DEFAULT_DOCK_LAYOUT in both cases,
// rather than leaving a blank workspace). Both persisted shapes load: the
// versioned wrapper and the bare dockview blob that predates versioning.
export function loadDockLayout(storage: DockStorage, key: string): LoadedLayout | null {
  const raw = storage.getItem(key);
  if (!raw) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && "v" in parsed && "layout" in parsed) {
      const sv = parsed as SavedLayout;
      return { layout: sv.layout, savedPanels: Array.isArray(sv.panels) ? sv.panels : [] };
    }
    return { layout: parsed, savedPanels: [] };
  } catch {
    return null;
  }
}

// panelsToReconcile returns registry panels absent from a restored layout's saved
// registry: panels a later feature added since the save. A panel the user closed
// stays closed (it is in savedPanels). The caller decides which of these to open
// (see RECONCILE_AUTO_OPEN) — a newly added observability panel is registered in
// the menu without being auto-opened over an existing arrangement.
export function panelsToReconcile(savedPanels: PanelId[]): PanelId[] {
  return DOCK_PANELS.filter((id) => !savedPanels.includes(id));
}

// resolveTheme maps the shell's theme state to a dockview theme name. The shell
// forces a theme with data-theme on <html> (light|dark); with none it follows
// the OS, which the caller passes in as prefersDark (prefers-color-scheme: dark).
export function resolveTheme(dataTheme: string | null, prefersDark: boolean): "dark" | "light" {
  if (dataTheme === "dark") return "dark";
  if (dataTheme === "light") return "light";
  return prefersDark ? "dark" : "light";
}
