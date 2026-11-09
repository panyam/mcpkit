import { expect, test } from "@playwright/test";
import { connectAgent, newGame, openPlayer } from "./helpers.js";

// A player who opens the page before any agent is around is told what to do,
// and what the page does on its own is visible.

test("a player with no game master is told how to start one", async ({ browser }) => {
  const game = newGame();
  const alice = await openPlayer(browser, "alice", game);
  const hint = alice.getByTestId("hint");

  await expect(hint).toBeVisible();
  await expect(hint).toContainText("No game master is connected yet");
  await expect(hint).toContainText("make gm");
  await expect(alice.getByTestId("tile-gate")).toContainText("click to search");
  await expect(alice.getByTestId("tile-market")).toContainText("click to walk here");
  await expect(alice.getByTestId("ctx-status")).toContainText("No context stream");

  await alice.getByTestId("ask").fill("Anyone there?");
  await alice.getByTestId("ask").press("Enter");
  await expect(alice.getByTestId("log")).toContainText("nobody heard that");

  const gm = await connectAgent("alice");
  await gm.callTool({ name: "attach", arguments: { resource: `lanternfall:${game}` } });
  await expect(hint).toContainText("Your game master is here");
  await alice.getByTestId("gm-switch").check();
  await expect(hint).toBeHidden();
  await gm.close();
});
