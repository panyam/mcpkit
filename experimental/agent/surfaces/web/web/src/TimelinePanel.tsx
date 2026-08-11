import { For, Show, createEffect } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { TimelineStore } from "./timeline.js";

// TimelinePanel renders the activity ledger: a kind filter across the top and a
// scrollable list of events below, newest at the bottom (it auto-pins to the
// tail as events stream). Fed by the TimelineStore projection off Watch (#1198).
export function Timeline(props: { store: TimelineStore }) {
  const s = props.store;
  let scroller!: HTMLDivElement;

  createEffect(() => {
    s.entries();
    queueMicrotask(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  return (
    <div class="obs obs-timeline">
      <div class="tl-filter">
        <label class="tl-filter-label">Filter</label>
        <select class="tl-select" value={s.filter()} onChange={(e) => s.setFilter(e.currentTarget.value)}>
          <option value="">all kinds</option>
          <For each={s.kinds()}>{(k) => <option value={k}>{k}</option>}</For>
        </select>
      </div>
      <div class="obs-list tl-log" ref={scroller}>
        <Show
          when={s.entries().length > 0}
          fallback={<div class="obs-empty">No events yet. The host's whole event stream lands here.</div>}
        >
          <For each={s.entries()}>
            {(e) => (
              <div class="tl-row">
                <span class="tl-kind">{e.kind}</span>
                <span class="tl-summary">{e.summary}</span>
              </div>
            )}
          </For>
        </Show>
      </div>
    </div>
  );
}

export function timelineIsland(el: HTMLElement, store: TimelineStore): SolidIsland {
  const island = new SolidIsland("timeline", el, () => <Timeline store={store} />, null);
  island.activate();
  return island;
}
