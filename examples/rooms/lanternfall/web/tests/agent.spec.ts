import { expect, test } from "@playwright/test";
import { runGameMaster } from "../src/agent/gm.js";
import { BASE, newGame, openPlayer } from "./helpers.js";

// Mission #1683's custom-agent check: a LangGraph.js agent with a system
// prompt of its own answers a question the player typed into the page, using
// only the tools the bridge lists. It calls Claude, so it runs only when
// ANTHROPIC_API_KEY is set, and says so when it skips.

test("a LangGraph game master reacts to a high-priority event and answers a question", async ({ browser }) => {
  test.skip(!process.env.ANTHROPIC_API_KEY, "ANTHROPIC_API_KEY is not set, so the custom agent cannot call a model");
  test.setTimeout(300_000);
  const game = newGame();
  const alice = await openPlayer(browser, "alice", game);
  await alice.getByTestId("gm-switch").check();

  const lines: string[] = [];
  const ctl = new AbortController();
  const gm = runGameMaster({ url: `${BASE}/mcp`, player: "alice", game, answers: 2, signal: ctl.signal, log: (l) => lines.push(l) });
  const gmSays = alice.getByTestId("log-gm");
  try {
    // Let the agent attach and start waiting: next_request returns events from then on.
    await expect.poll(() => lines.some((l) => l.startsWith("[gm] context hints")), { timeout: 30_000 }).toBe(true);
    await alice.waitForTimeout(1_500);

    // Walk to the Old Archive (medium-priority moves, kept as context), then
    // search it: item.found is high priority, so the game master reacts unasked.
    for (const place of ["market", "clocktower", "archive"]) await alice.getByTestId(`tile-${place}`).click();
    await alice.getByTestId("tile-archive").click();
    await expect(alice.getByTestId("inventory")).toContainText("torn ledger page");
    await expect(gmSays.first()).toBeVisible({ timeout: 120_000 });

    await alice.getByTestId("ask").fill("What does the ledger page say?");
    await alice.getByTestId("ask").press("Enter");
    await expect(gmSays.nth(1)).toBeVisible({ timeout: 120_000 });
    await gm;
  } finally {
    ctl.abort();
    console.log(lines.join("\n"));
  }
  expect(lines.some((l) => l.includes("item.found is high priority: reacting now"))).toBe(true);
  expect(lines.some((l) => l.includes("player.moved is medium priority: kept as context"))).toBe(true);
  expect(lines.filter((l) => l.includes("answered with") && l.includes("narrate"))).toHaveLength(2);
});
