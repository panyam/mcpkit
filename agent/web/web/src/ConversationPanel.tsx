import { For, Show, createSignal, createEffect } from "solid-js";
import { SolidIsland } from "@panyam/tsappkit-solid";
import type { ConversationStore } from "./conversation.js";

// Conversation renders the live turn: the committed transcript, the in-progress
// assistant text as it streams off Watch, an approval prompt when the host
// broadcasts an ask, and a prompt box that Submits. It is the one panel this
// slice ships (issue 1197); #1198 adds the observability panels beside it.
export function Conversation(props: { store: ConversationStore; compact?: boolean }) {
  const s = props.store;
  const [text, setText] = createSignal("");
  let scroller!: HTMLDivElement;
  let taRef!: HTMLTextAreaElement;

  // Keep the transcript pinned to the newest line as it streams.
  createEffect(() => {
    s.turns();
    s.streaming();
    queueMicrotask(() => scroller?.scrollTo({ top: scroller.scrollHeight }));
  });

  const grow = () => {
    if (!taRef) return;
    taRef.style.height = "auto";
    taRef.style.height = Math.min(taRef.scrollHeight, 160) + "px";
  };
  const send = () => {
    const q = text().trim();
    if (!q) return;
    void s.submit(q);
    setText("");
    grow();
  };
  const onKeyDown = (ev: KeyboardEvent) => {
    if (ev.key === "Enter" && !ev.shiftKey) {
      ev.preventDefault();
      send();
    }
  };

  return (
    <div class="conv" classList={{ "conv-compact": props.compact }}>
      <div class="conv-log" ref={scroller}>
        <For each={s.turns()}>
          {(t) => (
            <div class={`conv-turn conv-${t.role}`}>
              <span class="conv-role">
                {t.role}
                <Show when={t.from}>
                  <span class="conv-from"> · from {t.from}</span>
                </Show>
              </span>
              <div class="conv-text">{t.text}</div>
            </div>
          )}
        </For>
        <Show when={s.streaming()}>
          <div class="conv-turn conv-assistant conv-streaming">
            <span class="conv-role">
              assistant
              <Show when={s.fromOther()}>
                <span class="conv-from"> · from another surface</span>
              </Show>
            </span>
            <div class="conv-text">{s.streaming()}</div>
          </div>
        </Show>
        <Show when={s.activity()}>
          <div class="conv-activity">{s.activity()}</div>
        </Show>
        <Show when={s.error()}>
          <div class="conv-error">{s.error()}</div>
        </Show>
        <Show when={s.resolved()}>
          {(r) => (
            <div class="conv-resolved">
              <span class="conv-resolved-msg">{r().text}</span>
              <button type="button" class="conv-resolved-dismiss" aria-label="Dismiss" onClick={() => s.dismissResolved()}>
                ✕
              </button>
            </div>
          )}
        </Show>
      </div>

      <Show when={s.ask()}>
        {(a) => (
          <div class="conv-ask">
            <div class="conv-ask-msg">{a().message}</div>
            <div class="conv-ask-actions">
              <button type="button" class="conv-approve" onClick={() => void s.respondToAsk("accept")}>
                Approve
              </button>
              <button type="button" class="conv-decline" onClick={() => void s.respondToAsk("decline")}>
                Decline
              </button>
            </div>
          </div>
        )}
      </Show>

      <div class="conv-compose">
        <textarea
          ref={taRef}
          class="conv-ta"
          rows="1"
          value={text()}
          disabled={s.busy()}
          placeholder="Message the agent…  (Enter to send)"
          onInput={(e) => {
            setText(e.currentTarget.value);
            grow();
          }}
          onKeyDown={onKeyDown}
        />
        <button type="button" class="conv-send" disabled={!text().trim() || s.busy()} aria-label="Send" onClick={send}>
          ↑
        </button>
      </div>
    </div>
  );
}

// conversationIsland mounts the Conversation panel into el as a tsappkit-solid
// SolidIsland (the island shell diffpp uses). The island owns el's children
// while active, so el is a dedicated host — the DockView adopt path and the
// mobile overlay each hand it one. Returns the island so the caller disposes it.
export function conversationIsland(el: HTMLElement, store: ConversationStore, compact = false): SolidIsland {
  const island = new SolidIsland("conversation", el, () => <Conversation store={store} compact={compact} />, null);
  island.activate();
  return island;
}
