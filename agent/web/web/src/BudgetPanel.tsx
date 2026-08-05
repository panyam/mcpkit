import { SolidIsland } from "@panyam/tsappkit-solid";
import type { BudgetStore } from "./budget.js";

// BudgetPanel shows token + step gauges accumulated across turns, with a bar
// splitting cumulative input vs output tokens and a last-turn breakdown. Fed by
// the BudgetStore projection off the HostTurnDone stream (#1198).
function Tile(props: { label: string; value: string | number }) {
  return (
    <div class="bg-tile">
      <div class="bg-value">{props.value}</div>
      <div class="bg-label">{props.label}</div>
    </div>
  );
}

export function Budget(props: { store: BudgetStore }) {
  const s = props.store;
  const total = () => s.state().inputTokens + s.state().outputTokens;
  const inPct = () => {
    const t = total();
    return t > 0 ? Math.round((s.state().inputTokens / t) * 100) : 0;
  };
  return (
    <div class="obs obs-budget">
      <div class="bg-tiles">
        <Tile label="turns" value={s.state().turns} />
        <Tile label="steps" value={s.state().steps} />
        <Tile label="in tokens" value={s.state().inputTokens.toLocaleString()} />
        <Tile label="out tokens" value={s.state().outputTokens.toLocaleString()} />
      </div>
      <div class="bg-bar-label">
        input / output split ({total().toLocaleString()} total)
      </div>
      <div class="bg-bar" role="img" aria-label={`input ${inPct()}%`}>
        <div class="bg-bar-in" style={{ width: `${inPct()}%` }} />
        <div class="bg-bar-out" style={{ width: `${100 - inPct()}%` }} />
      </div>
      <div class="bg-last">
        last turn: {s.state().lastInput.toLocaleString()} in · {s.state().lastOutput.toLocaleString()} out · {s.state().lastSteps} step
        {s.state().lastSteps === 1 ? "" : "s"}
      </div>
    </div>
  );
}

export function budgetIsland(el: HTMLElement, store: BudgetStore): SolidIsland {
  const island = new SolidIsland("budget", el, () => <Budget store={store} />, null);
  island.activate();
  return island;
}
