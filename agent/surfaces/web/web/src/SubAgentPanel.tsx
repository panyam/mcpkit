import { For, Show } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { SubAgentStore, SubAgentStatus } from "./subagents.js";

// SubAgentPanel renders the sub-agent tree: one indented row per nested agent,
// its depth driving the indent, with a status dot, the latest activity line, and
// a tool-call count. It is fed by the SubAgentStore projection off Watch (issue
// 1198), and follows the ConversationPanel island pattern from #1197.

const STATUS_GLYPH: Record<SubAgentStatus, string> = {
  idle: "○",
  running: "◐",
  done: "●",
  error: "✗",
};

export function SubAgentTree(props: { store: SubAgentStore }) {
  const s = props.store;
  return (
    <div class="obs obs-subagents">
      <Show
        when={s.tree().length > 0}
        fallback={<div class="obs-empty">No sub-agent activity yet. Delegated agents appear here as a nested tree.</div>}
      >
        <div class="obs-list">
          <For each={s.tree()}>
            {(n) => (
              <div class="sa-row" style={{ "padding-left": `${(n.depth - 1) * 1.1 + 0.6}rem` }}>
                <span class={`sa-dot sa-${n.status}`} title={n.status}>
                  {STATUS_GLYPH[n.status]}
                </span>
                <span class="sa-name">{n.name}</span>
                <Show when={n.toolCalls > 0}>
                  <span class="sa-tools">{n.toolCalls} tool{n.toolCalls === 1 ? "" : "s"}</span>
                </Show>
                <span class="sa-activity">{n.resultText || n.activity}</span>
              </div>
            )}
          </For>
        </div>
      </Show>
    </div>
  );
}

// subAgentIsland mounts the panel into el (the DockView adopt path or a mobile
// overlay hands it a dedicated host). Returns the island so the caller disposes it.
export function subAgentIsland(el: HTMLElement, store: SubAgentStore): SolidIsland {
  const island = new SolidIsland("subagents", el, () => <SubAgentTree store={store} />, null);
  island.activate();
  return island;
}
