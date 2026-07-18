<!--
TEMPLATE.md — canonical README structure for mcpkit examples.

Used by every fixture under examples/apps/compat/ and meant for new examples
under examples/ generally. Copy this file into your fixture directory as
README.md and adapt:

  cp examples/common/TEMPLATE.md examples/apps/compat/<fixture>/README.md
  # or, for a non-compat example:
  cp examples/common/TEMPLATE.md examples/<your-example>/README.md

Then walk top-to-bottom and replace every {PLACEHOLDER} with fixture-specific
content. HTML comments (<!-- ... -->) are author-guidance only — strip them
before committing if any are still present.

Conditional sections: "## Run Pre-Recorded" and "## Try It Out from a Host"
each have a comment block above them — read those before deciding whether
to keep them.
-->

# {FIXTURE_NAME} — {ONE-LINE TAGLINE}

<!--
TAGLINE: half-sentence describing what makes this fixture distinct. Pulled
into the examples-ladder table on compat/README.md. Examples:
  - "minimum-viable MCP App in Go"
  - "same App, Preact iframe"
  - "deeply nested SaaS budget data"
  - "9-tool backend with per-viewUUID command queue"
-->

{LADDER_RUNG_AND_INTRO_PARAGRAPH}

<!--
INTRO: one paragraph naming the rung on the [examples ladder] and what
the fixture shows. Link to the closest neighbor for context. Example:

  Rung 4 on the [examples ladder](../README.md#reading-order--examples-ladder).
  One tool, but the output is a deeply nested object (config + analytics
  with multi-month history and stage benchmarks). First fixture where
  reflection of nested Go structs + maps produces the matching shape
  without override.
-->

## What it Shows

- **{POINT 1}** — {what's distinctive about this fixture's wire surface}
- **{POINT 2}** — {what's distinctive about this fixture's iframe / data}
- **{POINT 3}** — {what the reader should take away beyond "it works"}

<!--
Replace with 2-4 bullet points. Stay focused on what's NEW vs neighboring
fixtures — not "it's an MCP server with tools" (that's true of all of them).
Reference upstream parity, schema reflection, framework choice, payload
shape, etc.
-->

## Run Pre-Recorded

<!--
KEEP THIS SECTION if your example has walkthrough.go + walkthrough.trace.json
+ bundle/ committed. DELETE THE WHOLE SECTION (including this comment block
and the blockquote below) if you haven't authored a walkthrough yet.

The {EXAMPLE_PATH} placeholder is the example's directory relative to the
repo root (e.g. `examples/apps/compat/basic-vanillajs`, or
`examples/file-inputs`). The docs-site collector mirrors it 1:1 into the
published URL.
-->

> ▶ **[Play the walkthrough in your browser](https://panyam.github.io/mcpkit/walkthroughs/{EXAMPLE_PATH}/)** — animated playback of every curl / Go call the walkthrough makes, step-by-step. No clone, no setup.

## Or Run Live

### Start Server

```bash
just demo-app EXAMPLE={FIXTURE_NAME}
```

Starts the mcpkit-Go fixture on `http://localhost:3101/mcp` and basic-host on `http://localhost:8080`. (Pass `OPEN=1` to auto-open the browser.)

## Try It Out on basic-host

Open <http://localhost:8080> in your browser. Then:

1. Pick **{SERVER_INFO_NAME}** from the server dropdown.
2. Pick **{PRIMARY_TOOL_NAME}** from the tool dropdown, click **Call Tool** {WITH_OR_WITHOUT_INPUT}.
3. {WHAT_THE_USER_SEES — describe the iframe, any interaction, what the result looks like}

<!--
Add 1-3 screenshots that show the App rendered. Width=50% with click-to-zoom
is the convention:
-->

<a href="screenshots/01-{TOPIC}.png" target="_blank"><img src="screenshots/01-{TOPIC}.png" alt="{ALT_TEXT}" width="50%"></a>

## Try It Out from a Host

<!--
KEEP THIS SECTION for fixtures that have meaningful behavior when an
LLM is in the loop (most do). DROP IT for examples where there's no
"prompt the model" story (a wire-only demo, a CLI-only fixture).

The compat README backlink is compat-specific — drop the last sentence
for examples outside examples/apps/compat/.
-->

Connect to `http://localhost:3101/mcp` from your favorite MCP host — VS Code, Claude Desktop, [MCPJam Inspector](https://github.com/MCPJam/inspector), or any spec-compliant client.

**Prompts to try** (LLM-driven hosts):

> "{PROMPT 1}"
> "{PROMPT 2}"
> "{PROMPT 3}"

{ONE_SENTENCE — describe what the model will do with the response}

See [Other ways to test a fixture](../README.md#other-ways-to-test-a-fixture) in the compat README for wire inspection, upstream comparison, and the strict Playwright gate.

## What to Try Next

- {NEAREST_NEIGHBOR_ON_LADDER — what they should look at to deepen the lesson}
- {ANOTHER_USEFUL_POINTER — sibling rung, upstream comparison, etc.}
- See [`main.go`](main.go) — fixture is ~{ESTIMATED_LINE_COUNT} lines.
