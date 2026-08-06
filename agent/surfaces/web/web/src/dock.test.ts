import { describe, expect, it } from "vitest";
import {
  DOCK_PANELS,
  loadDockLayout,
  panelsToReconcile,
  resolveTheme,
  saveDockLayout,
  type DockStorage,
} from "./dock.js";

// memStorage is a DOM-free DockStorage for the persistence round-trip.
function memStorage(): DockStorage & { map: Map<string, string> } {
  const map = new Map<string, string>();
  return {
    map,
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => void map.set(k, v),
    removeItem: (k) => void map.delete(k),
  };
}

describe("dock layout persistence", () => {
  it("round-trips a saved layout stamped with the registry", () => {
    const st = memStorage();
    saveDockLayout(st, "k", { grid: 1 });
    const loaded = loadDockLayout(st, "k");
    expect(loaded?.layout).toEqual({ grid: 1 });
    expect(loaded?.savedPanels).toEqual([...DOCK_PANELS]);
  });

  it("returns null for an absent or corrupt layout", () => {
    const st = memStorage();
    expect(loadDockLayout(st, "missing")).toBeNull();
    st.map.set("bad", "{not json");
    expect(loadDockLayout(st, "bad")).toBeNull();
  });

  it("treats a bare pre-versioning blob as savedPanels=[]", () => {
    const st = memStorage();
    st.map.set("k", JSON.stringify({ grid: 2 }));
    expect(loadDockLayout(st, "k")).toEqual({ layout: { grid: 2 }, savedPanels: [] });
  });

  it("reconciles panels added since the save, leaving closed ones closed", () => {
    // A panel present in the registry but absent from the saved set is new.
    expect(panelsToReconcile([])).toEqual([...DOCK_PANELS]);
    // Everything already in the saved set stays as the user left it.
    expect(panelsToReconcile([...DOCK_PANELS])).toEqual([]);
  });
});

describe("resolveTheme", () => {
  it("honors a forced data-theme, else follows the OS", () => {
    expect(resolveTheme("dark", false)).toBe("dark");
    expect(resolveTheme("light", true)).toBe("light");
    expect(resolveTheme(null, true)).toBe("dark");
    expect(resolveTheme(null, false)).toBe("light");
  });
});
