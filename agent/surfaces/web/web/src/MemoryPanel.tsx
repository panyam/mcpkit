import { For, Show } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { MemoryStore } from "./memory.js";

// MemoryPanel shows the working-memory summary (read on demand via /memory) and
// the compaction history projected off the runner stream. Recall and injection
// are transient pre-turn transforms with no event, so the compaction log is the
// event-driven half; the summary button is the point-in-time read.
export function Memory(props: { store: MemoryStore }) {
  const s = props.store;
  return (
    <div class="obs obs-memory">
      <div class="mem-head">
        <span class="mem-title">Working memory</span>
        <button type="button" class="mem-refresh" disabled={s.refreshing()} onClick={() => void s.refresh()}>
          {s.refreshing() ? "reading…" : "read /memory"}
        </button>
      </div>
      <div class="mem-summary">
        <Show when={s.summary()} fallback={<span class="obs-empty">Read the model's remember/recall scratchpad.</span>}>
          <pre class="mem-pre">{s.summary()}</pre>
        </Show>
      </div>
      <div class="mem-section-label">Compactions</div>
      <div class="obs-list mem-compactions">
        <Show
          when={s.compactions().length > 0}
          fallback={<div class="obs-empty">No compaction yet. A pass appears here when history is summarized.</div>}
        >
          <For each={s.compactions()}>
            {(c) => (
              <div class="mem-row">
                <span class="mem-badge">compaction</span>
                <span class="mem-counts">
                  {c.before} → {c.after} msgs
                  <span class="mem-saved"> (−{Math.max(0, c.before - c.after)})</span>
                </span>
              </div>
            )}
          </For>
        </Show>
      </div>
    </div>
  );
}

export function memoryIsland(el: HTMLElement, store: MemoryStore): SolidIsland {
  const island = new SolidIsland("memory", el, () => <Memory store={store} />, null);
  island.activate();
  return island;
}
