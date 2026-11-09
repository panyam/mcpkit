import { expect, test } from "@playwright/test";
import { runGameMaster } from "../src/agent/gm.js";
import { BASE, newGame, openPlayer } from "./helpers.js";

// Mission #1683's custom-agent check: a LangGraph.js agent with a system
// prompt of its own answers a question the player typed into the page, using
// only the tools the bridge lists. It calls Claude, so it runs only when
// ANTHROPIC_API_KEY is set, and says so when it skips.

const CONTEXT_PORT = 18792;

test("a LangGraph game master reacts to a high-priority event, answers a question, and shows its context", async ({ browser }) => {
  test.skip(!process.env.ANTHROPIC_API_KEY, "ANTHROPIC_API_KEY is not set, so the custom agent cannot call a model");
  test.setTimeout(300_000);
  const game = newGame();
  const alice = await openPlayer(browser, "alice", game);
  // The same page, reading the game master's context stream on a test port.
  await alice.goto(`/?game=${game}&gm=${CONTEXT_PORT}`);
  await expect(alice.locator("body")).toHaveAttribute("data-ready", "true");
  await alice.getByTestId("gm-switch").check();

  const lines: string[] = [];
  const ctl = new AbortController();
  // A small budget, so the conversation compacts within the test.
  const gm = runGameMaster({
    url: `${BASE}/mcp`, player: "alice", game, answers: 2, budget: 400, contextPort: CONTEXT_PORT,
    signal: ctl.signal, log: (l) => lines.push(l),
  });
  const gmSays = alice.getByTestId("log-gm");
  try {
    await expect(alice.getByTestId("ctx-status")).toContainText("waiting for the player", { timeout: 30_000 });
    await alice.waitForTimeout(1_000);

    // Walk to the Old Archive: medium-priority moves land in the context as they happen.
    for (const place of ["market", "clocktower", "archive"]) await alice.getByTestId(`tile-${place}`).click();
    await expect(alice.getByTestId("ctx-event").filter({ hasText: "player.moved" })).toHaveCount(3);

    // Search it: item.found is high priority, so the game master reacts unasked.
    await alice.getByTestId("tile-archive").click();
    await expect(alice.getByTestId("inventory")).toContainText("torn ledger page");
    await expect(gmSays.first()).toBeVisible({ timeout: 120_000 });
    await expect(alice.getByTestId("ctx-meta")).toContainText("Compacted", { timeout: 60_000 });
    await expect(alice.getByTestId("ctx-summary")).toBeVisible();

    await alice.getByTestId("ask").fill("What does the ledger page say?");
    await alice.getByTestId("ask").press("Enter");
    await expect(gmSays.nth(1)).toBeVisible({ timeout: 120_000 });
    await expect(alice.getByTestId("ctx-tool_call").first()).toBeVisible();
    await gm;
  } finally {
    ctl.abort();
    console.log(lines.join("\n"));
  }
  expect(lines.some((l) => l.includes("item.found is high priority: reacting now"))).toBe(true);
  expect(lines.some((l) => l.includes("compacted"))).toBe(true);
  expect(lines.filter((l) => l.includes("answered with") && l.includes("narrate"))).toHaveLength(2);
});
