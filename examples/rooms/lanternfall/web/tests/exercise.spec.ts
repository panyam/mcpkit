import { expect, test } from "@playwright/test";
import { anyResult, connectAgent, eventCursor, newGame, nextEvent, openPlayer, pageId, textOf } from "./helpers.js";

// Mission #1683's scripted check: an agent drives a page the player opened in
// their own tab, through the room bridge, and hears what the player does
// there. Each step prints as it passes.

test("an agent plays game master on a player's own page", async ({ browser }) => {
  const game = newGame();
  const resource = `lanternfall:${game}`;
  const alice = await openPlayer(browser, "alice", game);
  const agent = await connectAgent("alice");

  await test.step("the server tells an agent with a fixed system prompt how to work the bridge", async () => {
    const instructions = agent.getInstructions() ?? "";
    expect(instructions).toContain("call attach with the resource");
    expect(instructions).toContain("Lanternfall");
  });

  await test.step("the agent attaches and sees the page's tools", async () => {
    await agent.callTool({ name: "attach", arguments: { resource } });
    const names = (await agent.listTools()).tools.map((t) => t.name);
    expect(names).toEqual(expect.arrayContaining(["look", "move_player", "reveal", "place_item", "narrate", "list_pages", "next_request"]));
  });

  await test.step("events/list shows the page's declared events with their context hints", async () => {
    const res: any = await agent.request({ method: "events/list", params: {} } as any, anyResult);
    const byName = Object.fromEntries((res.events ?? []).map((e: any) => [e.name, e]));
    expect(byName["item.found"]?._meta?.["io.github.panyam.mcpkit/context-hint"]).toMatchObject({ priority: "high" });
    expect(byName["place.examined"]?._meta?.["io.github.panyam.mcpkit/context-hint"]).toMatchObject({ priority: "low" });
    expect(byName["player.moved"]?.payloadSchema?.properties).toHaveProperty("place");
  });

  await test.step("the player turns the game master on", async () => {
    await alice.getByTestId("gm-switch").check();
    await expect
      .poll(async () => {
        const res: any = await agent.callTool({ name: "list_pages", arguments: { resource } });
        return (res.structuredContent?.pages ?? []).some((p: any) => p.allowed && p.default);
      })
      .toBe(true);
  });

  await test.step("a read-only call: look", async () => {
    const res: any = await agent.callTool({ name: "look", arguments: { resource } });
    expect(res.isError).toBeFalsy();
    expect(textOf(res)).toContain("Lantern Gate");
  });

  await test.step("acting calls change the page: move, reveal, narrate", async () => {
    await agent.callTool({ name: "move_player", arguments: { resource, place: "well" } });
    await expect(alice.getByTestId("tile-well").getByTestId("token")).toBeVisible();
    await agent.callTool({ name: "reveal", arguments: { resource, place: "well", detail: "The rope ends in a knot shaped like a crescent." } });
    await expect(alice.getByTestId("tile-well")).toContainText("crescent");
    await agent.callTool({ name: "narrate", arguments: { resource, text: "The willow leans closer, listening." } });
    await expect(alice.getByTestId("log")).toContainText("The willow leans closer");
    await expect(alice.getByTestId("feed")).toContainText('move_player({"place":"well"})');
    await expect(alice.getByTestId("feed-in").first()).toContainText("narrate");
  });

  await test.step("the player finds what the game master hid, and the agent hears item.found", async () => {
    await agent.callTool({ name: "place_item", arguments: { resource, place: "well", item: "a brass key" } });
    const from = await eventCursor(agent, resource);
    await alice.getByTestId("tile-well").click();
    await expect(alice.getByTestId("inventory")).toContainText("a brass key");
    const { event } = await nextEvent(agent, resource, "item.found", from);
    expect(event.data).toMatchObject({ item: "a brass key", place: "well" });
    expect(event.user).toBe("alice");
    const sent = alice.getByTestId("feed-out").filter({ hasText: "item.found" });
    await expect(sent).toContainText("high");
  });

  await test.step("resting the pointer on a place sends place.examined", async () => {
    const from = await eventCursor(agent, resource);
    await alice.getByTestId("tile-lighthouse").hover();
    await alice.waitForTimeout(900);
    const { event } = await nextEvent(agent, resource, "place.examined", from);
    expect(event.data).toMatchObject({ place: "lighthouse", name: "Lighthouse" });
  });

  await test.step("a question typed into the page reaches the agent as room.ask", async () => {
    const from = await eventCursor(agent, resource);
    await alice.getByTestId("ask").fill("What is behind the clocktower door?");
    await alice.getByTestId("ask").press("Enter");
    const { event } = await nextEvent(agent, resource, "room.ask", from);
    expect(event.data.text).toBe("What is behind the clocktower door?");
  });

  await test.step("a second player's tab, with the switch off, gets no calls", async () => {
    const bob = await openPlayer(browser, "bob", game);
    const bobId = await pageId(bob);
    const res: any = await agent.callTool({ name: "move_player", arguments: { resource, place: "pier", page: bobId } });
    expect(res.isError).toBe(true);
    await expect(bob.getByTestId("tile-gate").getByTestId("token")).toBeVisible();
    await expect(bob.getByTestId("log")).not.toContainText("leads you");
  });

  await agent.close();
});
