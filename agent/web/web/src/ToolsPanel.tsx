import { For, Show, createSignal } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { ToolStore, ToolCallEntry } from "./tools.js";

// ToolsPanel lists tool calls with their args, status, and result preview, and
// flags offloaded results (a stub + its ref). A row expands to show the full
// args/preview. Fed by the ToolStore projection off Watch (#1198). The offload
// blob is fetched internally by the model via read_tool_result and is not
// exposed on the web bridge, so an offloaded row shows the stub + ref, not the
// full blob.

const STATUS_CLASS: Record<ToolCallEntry["status"], string> = {
  running: "tool-running",
  ok: "tool-ok",
  error: "tool-error",
  denied: "tool-denied",
  cancelled: "tool-cancelled",
  unavailable: "tool-unavailable",
};

function Row(props: { call: ToolCallEntry }) {
  const [open, setOpen] = createSignal(false);
  const c = () => props.call;
  return (
    <div class="tool-row">
      <div class="tool-head" onClick={() => setOpen((o) => !o)}>
        <span class={`tool-status ${STATUS_CLASS[c().status]}`}>{c().status}</span>
        <span class="tool-name">{c().name}</span>
        <Show when={c().offloaded}>
          <span class="tool-offload" title={`offloaded, stored as ${c().ref}`}>
            offloaded · {c().ref}
          </span>
        </Show>
        <span class="tool-preview">{c().preview}</span>
      </div>
      <Show when={open()}>
        <div class="tool-detail">
          <Show when={c().args}>
            <div class="tool-detail-label">args</div>
            <pre class="tool-pre">{c().args}</pre>
          </Show>
          <Show when={c().preview}>
            <div class="tool-detail-label">{c().offloaded ? "stub (full blob fetched via read_tool_result)" : "result"}</div>
            <pre class="tool-pre">{c().preview}</pre>
          </Show>
        </div>
      </Show>
    </div>
  );
}

export function Tools(props: { store: ToolStore }) {
  const s = props.store;
  return (
    <div class="obs obs-tools">
      <Show when={s.offloadedCount() > 0}>
        <div class="tool-banner">{s.offloadedCount()} offloaded result{s.offloadedCount() === 1 ? "" : "s"}</div>
      </Show>
      <div class="obs-list">
        <Show
          when={s.calls().length > 0}
          fallback={<div class="obs-empty">No tool calls yet. Each call and its result appear here; large results show as an offload stub.</div>}
        >
          <For each={s.calls()}>{(c) => <Row call={c} />}</For>
        </Show>
      </div>
    </div>
  );
}

export function toolsIsland(el: HTMLElement, store: ToolStore): SolidIsland {
  const island = new SolidIsland("tools", el, () => <Tools store={store} />, null);
  island.activate();
  return island;
}
