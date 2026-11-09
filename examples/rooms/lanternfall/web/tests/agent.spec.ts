import { expect, test } from "@playwright/test";
import { runGameMaster } from "../src/agent/gm.js";
import { BASE, newGame, openPlayer } from "./helpers.js";

// Mission #1683's custom-agent check: a LangGraph.js agent with a system
// prompt of its own answers a question the player typed into the page, using
// only the tools the bridge lists. It calls Claude, so it runs only when
// ANTHROPIC_API_KEY is set, and says so when it skips.

test("a LangGraph game master answers a question asked from the page", async ({ browser }) => {
  test.skip(!process.env.ANTHROPIC_API_KEY, "ANTHROPIC_API_KEY is not set, so the custom agent cannot call a model");
  test.setTimeout(240_000);
  const game = newGame();
  const alice = await openPlayer(browser, "alice", game);
  await alice.getByTestId("gm-switch").check();

  const lines: string[] = [];
  const ctl = new AbortController();
  const gm = runGameMaster({ url: `${BASE}/mcp`, player: "alice", game, answers: 1, signal: ctl.signal, log: (l) => lines.push(l) });
  try {
    // Let the agent attach and start waiting before the player asks: next_request
    // returns events from when it starts waiting.
    await alice.waitForTimeout(3_000);
    await alice.getByTestId("ask").fill("I'm at the gate. Is there anything strange about the lantern here?");
    await alice.getByTestId("ask").press("Enter");
    await expect(alice.getByTestId("log-gm").first()).toBeVisible({ timeout: 180_000 });
    await gm;
  } finally {
    ctl.abort();
    console.log(lines.join("\n"));
  }
  expect(lines.some((l) => l.includes("answered with") && l.includes("narrate"))).toBe(true);
});
