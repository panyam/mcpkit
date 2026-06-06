package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// liveInteractionMaxWait is the upper-bound on the live-interaction step.
// User can press enter at any point to end the capture early (in
// interactive non-TUI mode); otherwise the step ends after this duration.
// Skipped entirely in --non-interactive mode.
const liveInteractionMaxWait = 30 * time.Second

// runDemo drives the demokit walkthrough against a server that the user
// started separately via `make serve`. Steps walk through every events-spec
// feature mcpkit currently supports: list, push, poll, cursorless, webhook
// + auto-refresh, all driven through the typed Go SDK at clients/go.
func runDemo() {
	serverURL := common.ServerURL()
	mcpURL := serverURL + "/mcp"
	injectURL := serverURL + "/inject"

	demo := demokit.New("MCP Events Extension — Discord reference walkthrough").
		Dir("events/discord").
		Description("Walks through the four delivery modes of the experimental MCP Events extension (events/list, push via SSE, poll, webhook with TTL refresh) plus the cursored vs cursorless source distinction. Webhook subscriber uses the typed Go SDK at experimental/ext/events/clients/go.").
		Actors(
			demokit.Actor("Host", "MCP Host (this client)"),
			demokit.Actor("Server", "MCP Server (make serve)"),
			demokit.Actor("Receiver", "Local webhook receiver (this process)"),
			demokit.Actor("Discord", "Discord (real bot mode only)"),
		)

	demo.Section("Setup — two modes",
		"This walkthrough runs against either a test-mode server or a real Discord bot.",
		"",
		"**Option A — Test mode** (no bot token needed). All steps run; the final live-interaction step skips with a 'no token' message. Drive synthetic events from a third terminal via `make inject` / `make inject-typing`.",
		"",
		"```",
		"Terminal 1:  make serve                                # server in test mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"Terminal 3:  make inject TEXT='hello'                  # message event",
		"             make inject-typing                        # typing event (cursorless)",
		"```",
		"",
		"**Option B — Real bot mode** (requires `DISCORD_BOT_TOKEN`). Same walkthrough plus the live step captures real typing + message events from your Discord channel. Token setup in the demo's README.",
		"",
		"```",
		"Terminal 1:  DISCORD_BOT_TOKEN=... make serve          # server in bot mode",
		"Terminal 2:  make demo                                 # this walkthrough",
		"             # In Discord: type, then send. Live step captures both.",
		"```",
	)

	demo.Section("What this demo covers",
		"- **events/list** — the source catalog, including the `cursorless` flag and the `_meta` per-type metadata field.",
		"- **Push** — long-lived SSE stream; `notifications/events/event` arrives in real time.",
		"- **Poll** — single-subscription `events/poll` (multi-sub batching is not supported).",
		"- **Cursorless source** — typing indicators that wire as `cursor: null`. Subscribers can't replay, only see live events.",
		"- **Source-side health signals** — `YieldError` (transient `notifications/events/error`, stream stays open).",
		"- **Webhook + auto-refresh** — `events/subscribe` with the typed `Subscription` + `Receiver[Data]` from `clients/go`. Includes the hardened delivery loop: dial-time SSRF guard, no-redirects, 256 KiB body cap with 413 non-retryable, Standard Webhooks signature scheme as default.",
		"- **Multi-subscription routing** — two subs to `discord.message` with different params; one event fans out to both, distinguished by `X-MCP-Subscription-Id` plus push-side `requestId` echo on every notification.",
		"- **Webhook delivery health** — `deliveryStatus` block on subscribe-refresh response after a failed delivery; suspend state machine flips Active=false after N consecutive failures and auto-Posts a `{type:terminated}` control envelope when run with `make serve-fast-suspend`.",
		"- **Auth posture** — `events/subscribe` requires an authenticated principal per spec; demo runs anonymously via `UnsafeAnonymousPrincipal`. Production deployments wire real OIDC and reject anonymous subscribes with `-32012 Forbidden`.",
		"- **Spec validation** — empty / malformed `delivery.secret` rejected; client-supplied `id` rejected; valid `whsec_` accepted with no secret echoed.",
		"",
		"Identity-mode subscribe and Standard Webhooks header naming are exercised by the unit tests in `experimental/ext/events/` and by `discord-events`'s e2e tests; they require the server to be started with mode flags so they're documented in the README rather than driven from this walkthrough.",
	)

	var (
		c             *client.Client
		messageCursor *string
	)

	// --- Step 1: Connect ---
	demo.Step("How do I open the conversation?").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Vanilla MCP `initialize` over Streamable HTTP. The events extension doesn't declare any new capability — `events/*` methods are registered server-side via the events library. Push delivery rides a long-lived per-subscription POST that returns SSE (`events/stream`), not the session GET back-channel, so the client doesn't need any transport-level wiring to receive events.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Vanilla MCP initialize over Streamable HTTP — mints the session id.
# The events extension declares no new capability; events/* are server-side methods.
SID=$(curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"x","version":"1"},"capabilities":{}}}' \
  -D - -o /dev/null | grep -i 'mcp-session-id' | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
echo "SID=$SID"`).Default(),
			demokit.MakeVariant("go", "go", `// Vanilla MCP initialize over Streamable HTTP — no new capability declared.
c := client.NewClient(mcpURL, core.ClientInfo{Name: "discord-events-host", Version: "1.0"})
if err := c.Connect(); err != nil {
    log.Fatalf("connect failed (start the server with: make serve): %v", err)
}
fmt.Printf("Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			c = client.NewClient(mcpURL,
				core.ClientInfo{Name: "discord-events-host", Version: "1.0"},
			)
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
				return
			}
			fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			return
		})

	// --- Step 2: events/list ---
	demo.Step("What kinds of events does this server even emit?").
		Arrow("Host", "Server", "events/list").
		DashedArrow("Server", "Host", "[discord.message (cursored), discord.typing (cursorless)]").
		Note(
			"`events/list` returns the catalog of event **types** the server can emit — not a list of recent event instances. (The naming is a touch misleading: it's much closer in spirit to `tools/list` than to a CRUD listing. Think of each entry as the schema for a kind of event that subscribers can ask for, not as data.)",
			"",
			"Each entry advertises a name, description, the supported delivery modes, an auto-derived `payloadSchema`, and the `cursorless` flag — `discord.message` buffers events and accepts replay cursors, `discord.typing` emits ephemerally and always wires `cursor: null`.",
			"",
			"- Each `EventDef` may carry an opaque `_meta` map for app-defined per-event-type metadata (mirrors the `_meta` convention on Tool / Resource / Prompt in base MCP). The same `_meta` convention applies on `EventOccurrence` (the wire-format Event envelope). The discord sources don't set `_meta` here; servers that want to surface trace ids, source-system tags, or other per-type annotations populate it on construction.",
			"- The events/list response carries an optional `nextCursor` for forward-compatible pagination (mirrors the tools/list / resources/list convention). Library doesn't paginate today (advertised sets are small in practice); the field is plumbed for forward compatibility.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# events/list returns the catalog of event TYPES (closer to tools/list than a CRUD listing).
# Each entry advertises name, deliveryModes, payloadSchema, the cursorless flag, and optional _meta.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":1,"method":"events/list","params":{}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// events/list returns the catalog of event TYPES, not recent instances.
res, err := c.Call("events/list", map[string]any{})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("events/list response:\n%s\n", string(res.Raw))`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			res, err := c.Call("events/list", map[string]any{})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/list response:\n%s\n", string(pretty))
			return
		})

	// --- Step 3: Push delivery via events/stream ---
	demo.Step("Can I get events as they happen?").
		Arrow("Host", "Server", "events/stream { name: discord.message }").
		DashedArrow("Server", "Host", "notifications/events/active { requestId, cursor }").
		Arrow("Receiver", "Server", "POST /inject (simulated Discord message)").
		DashedArrow("Server", "Host", "notifications/events/event { requestId, eventId, ... }").
		DashedArrow("Host", "Server", "(close request) → StreamEventsResult final frame").
		Note(
			"Yes — `events/stream` is the answer. It's a long-lived JSON-RPC request, one per subscription, that returns its events as `notifications/events/event` frames on the call's own SSE response stream. Spec §\"Push-Based Delivery\" L223-296.",
			"",
			"- Server confirms with notifications/events/active, then delivers events as notifications/events/event on the call's own SSE response stream.",
			"- Heartbeats fire every ≥30s carrying the source's current cursor so the client's persisted cursor advances during quiet periods.",
			"- Replaces the broadcast-to-all-listeners model from Phase 1; per-stream isolation comes for free since each stream is its own POST.",
			"- Typed Go SDK Stream() helper (experimental/ext/events/clients/go) threads the per-call notification hook (client.CallContext.WithNotifyHook) so callbacks fire only for THIS stream's notifications.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# events/stream is a long-lived JSON-RPC request; its events arrive as
# notifications/events/event frames on the call's own SSE response stream.
# Server first confirms with notifications/events/active { requestId, cursor }.
# (Inject a message from another terminal: make inject TEXT='hello')
curl -N -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"events/stream","params":{"name":"discord.message"}}'`).Default(),
			demokit.MakeVariant("go", "go", `// events/stream { name: discord.message } — long-lived request, one per subscription.
// The typed Stream() helper delivers notifications/events/event frames to OnEvent.
stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
    EventName: "discord.message",
    OnEvent:   func(ev events.Event) { gotEvent <- ev },
})
if err != nil {
    log.Fatalf("Stream open failed: %v", err)
}
defer stream.Stop()`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "alice",
				"text":       "hello from the walkthrough",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-gotEvent:
				if ev.Cursor != nil {
					cc := *ev.Cursor
					messageCursor = &cc
				}
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    notifications/events/event params:\n%s\n", string(pretty))
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no push notification within 3s\n")
			}
			return
		})

	// --- Step 4: Poll delivery ---
	demo.Step("What if I can't keep a long-lived stream open?").
		Arrow("Host", "Server", "events/poll {name: discord.message, cursor: <head>}").
		DashedArrow("Server", "Host", "{events: [], cursor: <head>, hasMore: false}").
		Note("Poll instead. `events/poll` is single-subscription per call (multi-sub batching was removed) with a flat top-level shape: `{name, params, cursor, maxAge, maxEvents}` in, `{events, cursor, hasMore, truncated, nextPollSeconds}` out. Polling at the head returns no new events but advances the cursor — the response shape is identical whether or not events are waiting, so the client's polling loop has one code path.").
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# events/poll is single-subscription per call with a flat top-level shape.
# Polling at the head returns no new events but advances the cursor — same shape either way.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"events/poll","params":{"name":"discord.message","cursor":"0"}}' | jq '.result'`).Default(),
			demokit.MakeVariant("go", "go", `// events/poll { name, cursor } — flat top-level shape, single subscription per call.
res, err := c.Call("events/poll", map[string]any{
    "name":   "discord.message",
    "cursor": cursor, // last-seen cursor, or "0" at the head
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("events/poll response:\n%s\n", string(res.Raw))`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			cursor := "0"
			if messageCursor != nil {
				cursor = *messageCursor
			}
			// Flat top-level shape per spec §"Poll-Based Delivery"
			// → "Request: events/poll" L139-149.
			res, err := c.Call("events/poll", map[string]any{
				"name":   "discord.message",
				"cursor": cursor,
			})
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			// Re-indent the raw response so the demo output shows the actual
			// wire shape — events/poll response per spec L139-149: flat
			// {events, cursor, hasMore, [truncated], [nextPollSeconds]}.
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/poll response:\n%s\n", string(pretty))
			return
		})

	// --- Step 5: Cursorless source ---
	demo.Step("What about events I don't need to replay, like 'user is typing'?").
		Arrow("Host", "Server", "events/stream { name: discord.typing }").
		DashedArrow("Server", "Host", "notifications/events/active { cursor: null }").
		Arrow("Receiver", "Server", "POST /inject?event=discord.typing").
		DashedArrow("Server", "Host", "notifications/events/event { cursor: null }").
		Note(
			"On the wire, the event type is marked cursorless: `events/list` advertises `cursorless: true` for that EventDef, every event delivery emits `cursor: null`, and `events/poll` always returns empty with `cursor: null` (there's nothing buffered to serve). Push delivery still fans out events live — the only thing that changes versus a cursored source is replay. (in mcpkit: source authors opt in via `events.NewYieldingSource[T](def, events.WithoutCursors())`)",
			"",
			"- Push delivery via events/stream still works — there's just nothing to replay.",
			"- Heartbeats also carry cursor:null (spec L294: \"null for event types that do not support replay\").",
			"- Useful for ephemeral state (typing indicators, presence, current readings).",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Same events/stream method, but discord.typing is a cursorless source:
# every delivery (and heartbeat) carries cursor:null — push still fans out live,
# only replay is unavailable. (Inject from another terminal: make inject-typing)
curl -N -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"events/stream","params":{"name":"discord.typing"}}'`).Default(),
			demokit.MakeVariant("go", "go", `// events/stream { name: discord.typing } — cursorless source, every event wires cursor:null.
stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
    EventName: "discord.typing",
    OnEvent:   func(ev events.Event) { gotEvent <- ev }, // ev.Cursor stays nil
})
if err != nil {
    log.Fatalf("Stream open failed: %v", err)
}
defer stream.Stop()`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotEvent := make(chan events.Event, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.typing",
				OnEvent:   func(ev events.Event) { gotEvent <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"user":       "alice",
			}
			if err := postInject(injectURL, "discord.typing", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-gotEvent:
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    notifications/events/event params:\n%s\n", string(pretty))
				if ev.Cursor != nil {
					fmt.Printf("    UNEXPECTED: cursorless events should wire as cursor:null\n")
				}
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no typing event within 3s\n")
			}
			return
		})

	// --- Step 5.5: Source-side health signals ---
	demo.Step("What happens when the upstream source has a hiccup?").
		Arrow("Host", "Server", "events/stream { name: discord.message }").
		DashedArrow("Server", "Host", "notifications/events/active").
		Arrow("Receiver", "Server", "POST /inject?action=error").
		DashedArrow("Server", "Host", "notifications/events/event/error { requestId, error: { code, message } }").
		Note(
			"On the wire, two notification methods carry source health. `notifications/events/error` (spec L255+L261) is transient — the source had a failure, the stream stays open, subsequent events still arrive. `notifications/events/terminated` (spec L783-795) is terminal — the subscription has ended. This step exercises the transient path: `inject?action=error` causes the source to surface one upstream failure, the open stream sees `notifications/events/error` arrive while staying connected. (in mcpkit: server authors trigger these via `source.YieldError(err)` / `source.YieldTerminated(err)`)",
			"",
			"- Webhook subscribers don't see error envelopes (errors are upstream-side, not delivery-side); they DO see {type:terminated} control envelopes when the suspend state machine flips Active=false or when the source itself terminates.",
			"- This walkthrough step exercises only the transient error path — calling `inject?action=terminate` would one-shot terminate the discord.message source, breaking subsequent walkthrough steps that depend on it. Full terminate flow is covered by TestE2EHealthSignalsEndToEnd in this demo's e2e_test.go.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Open an events/stream, then trigger a transient upstream failure on the source.
# notifications/events/error arrives on the open stream — which stays connected.
# (notifications/events/terminated would be terminal; this path is transient only.)
curl -N -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":5,"method":"events/stream","params":{"name":"discord.message"}}' &
curl -s -X POST 'http://localhost:8080/inject?action=error&code=-32603&message=demo+upstream+failure'`).Default(),
			demokit.MakeVariant("go", "go", `// events/stream { name: discord.message } — watch the source-health channel.
// A transient YieldError surfaces via OnError; the stream stays open.
stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
    EventName: "discord.message",
    OnError:   func(e error) { gotError <- e }, // notifications/events/error (transient)
})
if err != nil {
    log.Fatalf("Stream open failed: %v", err)
}
defer stream.Stop()`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			gotError := make(chan error, 4)
			stream, err := eventsclient.Stream(ctx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnError:   func(e error) { gotError <- e },
			})
			if err != nil {
				fmt.Printf("    ERROR: Stream open failed: %v\n", err)
				return
			}
			defer stream.Stop()

			// Inject a transient upstream error.
			injectErrURL := injectURL + "?action=error&code=-32603&message=demo+upstream+failure"
			req, _ := http.NewRequest("POST", injectErrURL, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}
			resp.Body.Close()

			select {
			case e := <-gotError:
				fmt.Printf("    notifications/events/error fired:\n      %v\n", e)
				fmt.Printf("    Stream is still open (transient): subsequent events would still arrive.\n")
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no notifications/events/error within 3s\n")
			}
			return
		})

	// --- Step 6: Webhook + auto-refresh via Go SDK ---
	demo.Step("What if my client itself keeps restarting, but I have a public callback URL?").
		Arrow("Receiver", "Receiver", "spin up local httptest receiver on :random").
		Arrow("Host", "Server", "events/subscribe { mode: webhook, url, secret: whsec_<client-supplied> }").
		DashedArrow("Server", "Host", "{ id, refreshBefore }   (response does NOT echo secret per spec)").
		Arrow("Receiver", "Server", "POST /inject (simulated message)").
		DashedArrow("Server", "Receiver", "POST <url> + HMAC signature headers (default webhook-* per Standard Webhooks, opt-in X-MCP-* via -webhook-header-mode mcp)").
		DashedArrow("Host", "Host", "background loop: re-subscribe at 0.5 × TTL").
		Note(
			"Use webhook delivery. `events/subscribe` registers a callback URL plus a client-supplied `whsec_` secret with a TTL; the server POSTs HMAC-signed events to that URL as they happen, the subscription is soft-state on the server (in-memory with TTL), and the client refreshes before `refreshBefore` to keep it alive. If the client process dies and reconnects later with the same canonical tuple, the subscription either is still alive (refresh is idempotent) or has lapsed and the next subscribe creates a fresh one with the supplied cursor as the replay point. (in mcpkit: `clients/go` provides `Subscription` for subscribe + auto-refresh and `Receiver[Data]` for a typed inbound channel)",
			"",
			"- HMAC signing secret is client-supplied per spec; SDK auto-generates a whsec_ value via events.GenerateSecret() when SubscribeOptions.Secret is empty.",
			"- Subscription.Secret() returns the value the SDK ended up using, so the receiver can verify with the same secret.",
			"- Receiver[DiscordEventData] verifies signatures and decodes the wire envelope into the typed Data shape — consumer reads `ev.Data.Content` directly, no re-parsing JSON.",
			"- Default header mode is **Standard Webhooks** (spec L390+L431): `webhook-id` / `webhook-timestamp` / `webhook-signature: v1,<base64>` plus the MCP-specific `X-MCP-Subscription-Id`. Off-the-shelf Svix-style verifiers work. `MCPHeaders` (`X-MCP-Signature` + `X-MCP-Timestamp`) is the opt-in legacy via `-webhook-header-mode mcp`.",
			"",
			"**Hardened delivery loop** (`webhook.go` `deliver()`):",
			"",
			"- Dial-time SSRF guard rejects loopback / RFC1918 / link-local / IPv6-ULA / multicast at the `net.Dialer.Control` callback (TOCTOU-safe under DNS rebinding). The demo bypasses this via `WithWebhookAllowPrivateNetworks(true)` because it delivers to a local httptest receiver; production deployments leave the guard ON. Spec §\"Webhook Security\" L464.",
			"- No-redirect-following: `http.Client.CheckRedirect` returns `ErrUseLastResponse` so a receiver returning 3xx to an internal address can't bypass the dial-time guard via Go's redirect chain. 3xx is terminal `http_3xx_redirect`.",
			"- 256 KiB body cap (REJECT not TRUNCATE — truncation would corrupt the HMAC); 413 from the receiver is non-retryable. Spec L487.",
			"- 5xx retry with exponential backoff (4 attempts: 500ms → 1s → 2s → 5s cap). Standard webhook convention.",
			"",
			"**Auth posture:** `events/subscribe` requires an authenticated principal per spec §\"Subscription Identity\" → \"Authentication required\" L361. The demo runs anonymously via `events.Config.UnsafeAnonymousPrincipal=\"demo-user\"` (logged at startup as \"auth: demo (anonymous → UnsafeAnonymousPrincipal)\"). Production deployments unset that field AND wire `server.WithAuth(JWTValidator)` so anonymous subscribes hit the spec-mandated `-32012 Forbidden`. See README \"Auth posture: demo escape vs real OIDC\".",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# events/subscribe registers a callback URL + a client-supplied whsec_ secret with a TTL.
# Response carries { id, refreshBefore } and does NOT echo the secret (spec).
# Tear down by tuple (name, params, delivery.url) — the derived id is not accepted as input.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":6,"method":"events/subscribe","params":{"name":"discord.message","delivery":{"mode":"webhook","url":"https://receiver.example/hook","secret":"whsec_<client-supplied>"},"maxAge":300}}' | jq '.result'
# later: events/unsubscribe { name, delivery: { url } }`).Default(),
			demokit.MakeVariant("go", "go", `// events/subscribe via the typed SDK: subscribe + background auto-refresh at 0.5xTTL.
// Secret auto-generated when empty; Receiver[Data] decodes the typed webhook envelope.
sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
    EventName:     "discord.message",
    CallbackURL:   hookSrv.URL,
    RefreshFactor: 0.5,
    MaxAge:        5 * time.Minute, // bound worst-case replay on reconnect
})
if err != nil {
    log.Fatalf("subscribe failed: %v", err)
}
defer sub.Stop()
// Tear down by tuple (the derived id is not accepted as input):
defer c.Call("events/unsubscribe", map[string]any{
    "name":     "discord.message",
    "delivery": map[string]any{"url": hookSrv.URL},
})`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			recv := eventsclient.NewReceiver[DiscordEventData]("")
			defer recv.Close()

			hookSrv := httptest.NewServer(recv)
			defer hookSrv.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var refreshes int32
			sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
				EventName:     "discord.message",
				CallbackURL:   hookSrv.URL,
				RefreshFactor: 0.5,
				OnRefresh:     func() { atomic.AddInt32(&refreshes, 1) },
				// Bound worst-case replay on reconnect to 5 minutes
				// per spec §"Cursor Lifecycle" → "Bounding replay
				// with maxAge" L529. Stored on WebhookTarget for
				// future reconnect-with-replay logic.
				MaxAge: 5 * time.Minute,
			})
			if err != nil {
				fmt.Printf("    ERROR: subscribe failed: %v\n", err)
				return
			}
			// sub.Stop() ends the refresh loop but does NOT unsubscribe
			// server-side — the registry retains the subscription for its
			// TTL (~5 min) and keeps retrying delivery to the now-closed
			// httptest port. Eager unsubscribe avoids the noisy "connection
			// refused" retry log on the server side after the demo ends.
			defer func() {
				sub.Stop()
				// Unsubscribe by tuple (spec §"Unsubscribing:
				// events/unsubscribe" L509) — (name, params,
				// delivery.url). The derived id is not accepted
				// as input.
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "discord.message",
					"delivery": map[string]any{"url": hookSrv.URL},
				})
			}()
			recv.SetSecret(sub.Secret())
			fmt.Printf("    SDK-generated secret:    %s...\n", truncate(sub.Secret(), 16))
			fmt.Printf("    refreshBefore:           %s\n", sub.RefreshBefore().Format(time.RFC3339))

			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "bob",
				"text":       "hello via webhook",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			select {
			case ev := <-recv.Events():
				pretty, _ := json.MarshalIndent(ev, "    ", "  ")
				fmt.Printf("    webhook delivery (typed Event[DiscordEventData]):\n%s\n", string(pretty))
			case <-time.After(3 * time.Second):
				fmt.Printf("    ERROR: no webhook delivery within 3s\n")
				return
			}

			fmt.Printf("    on_refresh callbacks fired so far: %d (initial subscribe + any auto-refreshes)\n",
				atomic.LoadInt32(&refreshes))
			return
		})

	// --- Step 6.5: Multi-subscription routing (X-MCP-Subscription-Id + requestId echo) ---
	demo.Step("Two subs to the same event with different params — how do I tell deliveries apart?").
		Arrow("Host", "Server", "events/subscribe { name: discord.message, params: {channel_id: 'alpha'}, ... }").
		DashedArrow("Server", "Host", "{ id: sub_<A>, ... }").
		Arrow("Host", "Server", "events/subscribe { name: discord.message, params: {channel_id: 'beta'}, ... }").
		DashedArrow("Server", "Host", "{ id: sub_<B>, ... }   (id differs from A — different params → different canonical tuple)").
		Arrow("Receiver", "Server", "POST /inject (one event)").
		DashedArrow("Server", "Receiver", "POST <url> + X-MCP-Subscription-Id: sub_<A>").
		DashedArrow("Server", "Receiver", "POST <url> + X-MCP-Subscription-Id: sub_<B>").
		Note(
			"Each delivery POST carries its own `X-MCP-Subscription-Id` header (per spec §\"Webhook Event Delivery\" L390), and on the push side every notification echoes the originating `events/stream` request id in `params.requestId`. Subscriptions are identified by the canonical tuple `(principal, delivery.url, name, params)` (spec §\"Subscription Identity\" → \"Key composition\" L363), so two subscribes with the same `(principal, url, name)` but different `params` produce different ids — and the receiver branches by header without parsing the body.",
			"",
			"- The library fans out one yielded event to **both** webhook targets by default. Authors that want per-subscription filtering attach a `Match` (and optionally `Transform`) hook on the `EventDef` — the hook fires once per (event × subscription) on the fanout step and short-circuits delivery for non-matching subs. The discord demo doesn't wire one because params-based routing (different ids per `(name, params)` tuple) is enough for the \"two subs, same event, different params\" story.",
			"- Push side: the same routing works via the `requestId` echo on every `notifications/events/event` payload — each `events/stream` POST gets its own JSON-RPC id, and notifications carry it in `params.requestId`.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Two subscribes, same (principal, url, name) but different params → different ids.
# One event then fans out to both; each delivery POST carries its own X-MCP-Subscription-Id.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":7,"method":"events/subscribe","params":{"name":"discord.message","params":{"channel_id":"alpha"},"delivery":{"mode":"webhook","url":"https://receiver.example/hook","secret":"whsec_<a>"}}}' | jq '.result.id'
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":8,"method":"events/subscribe","params":{"name":"discord.message","params":{"channel_id":"beta"},"delivery":{"mode":"webhook","url":"https://receiver.example/hook","secret":"whsec_<b>"}}}' | jq '.result.id'
# later: events/unsubscribe per tuple — { name, params: { channel_id }, delivery: { url } }`).Default(),
			demokit.MakeVariant("go", "go", `// Two events/subscribe to the same name+url but different params → distinct ids.
// One yielded event fans out to both targets (no per-sub match filter yet).
res, err := c.Call("events/subscribe", map[string]any{
    "name":   "discord.message",
    "params": map[string]any{"channel_id": channelLabel}, // "alpha" then "beta"
    "delivery": map[string]any{
        "mode":   "webhook",
        "url":    recv.URL,
        "secret": events.GenerateSecret(),
    },
})
if err != nil {
    log.Fatal(err)
}
// res.Raw → { "id": "sub_...", ... }; the two labels yield different ids.`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			// One receiver, two subs. Capture each delivery's
			// X-MCP-Subscription-Id to show they differ.
			type delivery struct {
				SubID string
				Body  []byte
			}
			gotDelivery := make(chan delivery, 8)
			recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotDelivery <- delivery{
					SubID: r.Header.Get("X-MCP-Subscription-Id"),
					Body:  body,
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer recv.Close()

			subscribe := func(channelLabel string) (string, error) {
				supplied := events.GenerateSecret()
				res, err := c.Call("events/subscribe", map[string]any{
					"name":   "discord.message",
					"params": map[string]any{"channel_id": channelLabel},
					"delivery": map[string]any{
						"mode":   "webhook",
						"url":    recv.URL,
						"secret": supplied,
					},
				})
				if err != nil {
					return "", err
				}
				var body struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(res.Raw, &body)
				return body.ID, nil
			}

			subA, err := subscribe("alpha")
			if err != nil {
				fmt.Printf("    ERROR: subscribe alpha: %v\n", err)
				return
			}
			subB, err := subscribe("beta")
			if err != nil {
				fmt.Printf("    ERROR: subscribe beta: %v\n", err)
				return
			}
			fmt.Printf("    sub_alpha id: %s\n", subA)
			fmt.Printf("    sub_beta  id: %s\n", subB)
			if subA == subB {
				fmt.Printf("    UNEXPECTED: ids should differ — different params → different canonical tuple\n")
				return
			}

			// Eager unsubscribe on both at the end.
			defer func() {
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "discord.message",
					"params":   map[string]any{"channel_id": "alpha"},
					"delivery": map[string]any{"url": recv.URL},
				})
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "discord.message",
					"params":   map[string]any{"channel_id": "beta"},
					"delivery": map[string]any{"url": recv.URL},
				})
			}()

			// One inject; the library should fan out to both registered
			// targets (no match filter yet).
			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "carol",
				"text":       "hello to both subs",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			seen := map[string]bool{}
			deadline := time.After(4 * time.Second)
			for len(seen) < 2 {
				select {
				case d := <-gotDelivery:
					seen[d.SubID] = true
					fmt.Printf("    delivery #%d  X-MCP-Subscription-Id=%s  bytes=%d\n",
						len(seen), d.SubID, len(d.Body))
				case <-deadline:
					fmt.Printf("    ERROR: only saw %d/2 deliveries within 4s\n", len(seen))
					return
				}
			}

			if seen[subA] && seen[subB] {
				fmt.Printf("    Both sub ids observed in delivery headers — routing-handle works.\n")
			} else {
				fmt.Printf("    UNEXPECTED: did not observe both sub ids; saw %v\n", seen)
			}
			return
		})

	// --- Step 6.7: Webhook delivery health (deliveryStatus + suspend) ---
	demo.Step("My webhook receiver just died. How does the server let me know?").
		Arrow("Receiver", "Receiver", "spin up failing receiver (returns 500 on event POSTs)").
		Arrow("Host", "Server", "events/subscribe { name: discord.message, ... }").
		DashedArrow("Server", "Host", "{ id, refreshBefore }   (no deliveryStatus on first subscribe — nothing to report)").
		Arrow("Receiver", "Server", "POST /inject (one event)").
		DashedArrow("Server", "Receiver", "POST <url>  → 500  (×4 retries with exponential backoff, then recordDeliveryFailure)").
		Arrow("Host", "Server", "events/subscribe (refresh — same canonical tuple)").
		DashedArrow("Server", "Host", "{ id, refreshBefore, deliveryStatus: { active, lastDeliveryAt, lastError, failedSince } }").
		DashedArrow("Server", "Receiver", "(if suspend fires) POST <url> body={type:terminated, error}  + webhook-id=msg_terminated_<random>").
		Note(
			"Two answers, layered. First, every subscribe-refresh response carries a `deliveryStatus` block when the target has prior delivery attempts (spec §\"Webhook Delivery Status\" L425-460): `active` / `lastDeliveryAt` / `lastError` / `failedSince`. Second, after N consecutive failures within a sliding window, the server flips `active: false` and auto-Posts a `{type:terminated}` control envelope to the receiver as a courtesy heads-up. Refresh of a suspended target reactivates it.",
			"",
			"- `lastError` is from a **closed categorical set** (`connection_refused`, `timeout`, `tls_error`, `http_3xx_redirect`, `http_4xx`, `http_5xx`, `challenge_failed`); the spec forbids raw response bodies / headers / status lines because the subscribe response is visible to the subscriber and arbitrary receiver responses must not become a data oracle.",
			"- `failedSince` is set on the **first failure of the current run** and preserved across subsequent failures, so subscribers can see how long the receiver has been unreachable.",
			"- Spec §\"Webhook Event Delivery\" L413+L460: \"after repeated failures the server SHOULD set active: false.\" The transition fires after 5 consecutive failures within a 10-min sliding window. On the `true→false` transition the server auto-Posts a `{type:terminated}` control envelope to the (now-suspended) receiver — `webhook-id` prefix is `msg_terminated_<random>` so receivers can distinguish it from event deliveries (which use `evt_<eventId>`). (in mcpkit: knobs are `events.WithWebhookSuspendThreshold(n)` and `events.WithWebhookSuspendWindow(d)`)",
			"- A successful refresh of a suspended target reactivates it: clears the failure run, resets `lastError` and `failedSince`, flips `active` back to true.",
			"",
			"**Fast-mode tip:** with the default `make serve` (`-webhook-suspend-threshold 5`), this step demonstrates the deliveryStatus reporting (lastError populated, failedSince populated, active still true) — full suspend takes 5 failed deliveries × ~8.5s each. To see suspend fire after ONE failure (~12s total step time), restart the server with `make serve-fast-suspend` (sets `-webhook-suspend-threshold 1`).",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# First subscribe has no deliveryStatus (nothing to report yet).
# After a failed delivery, re-subscribe on the SAME tuple → response carries
# deliveryStatus { active, lastDeliveryAt, lastError, failedSince }.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":9,"method":"events/subscribe","params":{"name":"discord.message","params":{"role":"health-demo"},"delivery":{"mode":"webhook","url":"https://dead-receiver.example/hook","secret":"whsec_<v>"}}}' | jq '.result'
# (inject an event, let the ~8.5s retry cycle exhaust, then re-issue the SAME subscribe to see deliveryStatus)`).Default(),
			demokit.MakeVariant("go", "go", `// events/subscribe twice on the same canonical tuple: initial, then refresh.
// The refresh response carries a deliveryStatus block once a delivery has failed.
subParams := map[string]any{
    "name":   "discord.message",
    "params": map[string]any{"role": "health-demo"},
    "delivery": map[string]any{
        "mode":   "webhook",
        "url":    deadReceiver.URL,
        "secret": events.GenerateSecret(),
    },
}
res, err := c.Call("events/subscribe", subParams) // initial: no deliveryStatus
if err != nil {
    log.Fatal(err)
}
_ = res
// ... inject one event, wait out the retry cycle (~8.5s) ...
res2, err := c.Call("events/subscribe", subParams) // refresh: res2.Raw has deliveryStatus
if err != nil {
    log.Fatal(err)
}
_ = res2`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			// Receiver returns 500 on event deliveries (forces failures);
			// returns 200 on control-envelope POSTs (and captures them).
			// Discriminator: webhook-id prefix `evt_` = event,
			// `msg_terminated_` = control. Per webhook.go control.go.
			var gotControl atomic.Bool
			var controlBody []byte
			deadReceiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				whID := r.Header.Get("webhook-id")
				if strings.HasPrefix(whID, "msg_terminated_") {
					body, _ := io.ReadAll(r.Body)
					controlBody = body
					gotControl.Store(true)
					w.WriteHeader(http.StatusOK)
					return
				}
				// Event delivery — simulate persistent receiver-side failure.
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer deadReceiver.Close()

			supplied := events.GenerateSecret()
			subParams := map[string]any{
				"name":   "discord.message",
				"params": map[string]any{"role": "health-demo"},
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    deadReceiver.URL,
					"secret": supplied,
				},
			}

			// Initial subscribe — first call has no priorities, no
			// deliveryStatus block per spec L548 ("Omitted on first
			// subscribe — there's nothing to report").
			res, err := c.Call("events/subscribe", subParams)
			if err != nil {
				fmt.Printf("    ERROR: initial subscribe failed: %v\n", err)
				return
			}
			defer func() {
				_, _ = c.Call("events/unsubscribe", map[string]any{
					"name":     "discord.message",
					"params":   map[string]any{"role": "health-demo"},
					"delivery": map[string]any{"url": deadReceiver.URL},
				})
			}()
			var firstResp map[string]any
			_ = json.Unmarshal(res.Raw, &firstResp)
			if _, hasStatus := firstResp["deliveryStatus"]; hasStatus {
				fmt.Printf("    UNEXPECTED: deliveryStatus present on first subscribe (spec L548 says it's omitted)\n")
			} else {
				fmt.Printf("    First subscribe: no deliveryStatus block (correct per spec — nothing to report)\n")
			}

			// Inject one event — server will retry 4 times with exponential
			// backoff (500ms / 1s / 2s / 5s cap), then recordDeliveryFailure.
			fmt.Printf("    Injecting one event; waiting for retry cycle to exhaust (~8.5s)...\n")
			body := map[string]any{
				"guild_id":   "demo-guild",
				"channel_id": "demo-channel",
				"sender":     "dave",
				"text":       "hello to a dead receiver",
			}
			if err := postInject(injectURL, "discord.message", body); err != nil {
				fmt.Printf("    ERROR: %v\n", err)
				return
			}

			// Wait for the retry cycle: initial + 3 retries with 0.5/1/2/5s
			// backoff = ~8.5s. Plus a small buffer.
			time.Sleep(10 * time.Second)

			// Re-subscribe (refresh) on same canonical tuple — response
			// should now carry deliveryStatus per spec L425-460.
			res2, err := c.Call("events/subscribe", subParams)
			if err != nil {
				fmt.Printf("    ERROR: refresh subscribe failed: %v\n", err)
				return
			}
			var refreshResp map[string]any
			_ = json.Unmarshal(res2.Raw, &refreshResp)
			pretty, _ := json.MarshalIndent(refreshResp, "    ", "  ")
			fmt.Printf("    Refresh response (note deliveryStatus block):\n%s\n", string(pretty))

			status, _ := refreshResp["deliveryStatus"].(map[string]any)
			if status == nil {
				fmt.Printf("    ERROR: deliveryStatus missing — server should populate it after a failed delivery\n")
				return
			}
			active, _ := status["active"].(bool)
			fmt.Printf("    deliveryStatus.active=%v  lastError=%v  failedSince=%v\n",
				active, status["lastError"], status["failedSince"])
			if active {
				fmt.Printf("    Suspend has NOT fired (default threshold = 5; need 5 failed deliveries).\n")
				fmt.Printf("    For a fast suspend demo, restart server with `make serve-fast-suspend`\n")
				fmt.Printf("    (sets -webhook-suspend-threshold 1 so ONE failure flips Active=false).\n")
			} else {
				fmt.Printf("    Suspend FIRED — Active=false. Refresh of this subscription will reactivate.\n")
				if gotControl.Load() {
					fmt.Printf("    {type:terminated} control envelope was POSTed to the receiver:\n")
					var env map[string]any
					_ = json.Unmarshal(controlBody, &env)
					envPretty, _ := json.MarshalIndent(env, "      ", "  ")
					fmt.Printf("%s\n", string(envPretty))
				} else {
					fmt.Printf("    (Control envelope POST was attempted; receiver may not have captured it within the wait window — re-run if needed.)\n")
				}
			}

			return
		})

	// --- Step 7: Spec validation — empty secret rejected ---
	demo.Step("What if I forget the secret?").
		Arrow("Host", "Server", "events/subscribe { delivery: { ... } }   (no secret)").
		DashedArrow("Server", "Host", "-32602 InvalidParams: delivery.secret is required").
		Note(
			"Rejected with `-32602 InvalidParams` at subscribe time. `delivery.secret` is REQUIRED on every `events/subscribe` per spec — there's no server-side fallback. Rejecting at subscribe time means a malformed subscription never exists in the registry, so the server can't ever produce unverifiable deliveries.",
			"",
			"- This step makes a raw client.Call to bypass the SDK and demonstrate the server-side validator directly. (in mcpkit: the Go SDK auto-generates a conforming whsec_ value via `events.GenerateSecret()` when `SubscribeOptions.Secret` is empty — this step skips that on purpose)",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# delivery.secret is REQUIRED on every events/subscribe — no server-side fallback.
# Omitting it is rejected at subscribe time with -32602 InvalidParams.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":10,"method":"events/subscribe","params":{"name":"discord.message","delivery":{"mode":"webhook","url":"http://localhost:1/sink"}}}' | jq '.error'`).Default(),
			demokit.MakeVariant("go", "go", `// Raw call with no delivery.secret → server returns -32602 InvalidParams.
_, err := c.Call("events/subscribe", map[string]any{
    "name": "discord.message",
    "delivery": map[string]any{
        "mode": "webhook",
        "url":  "http://localhost:1/sink",
    },
})
rpcErr := err.(*client.RPCError) // code == -32602, delivery.secret is required`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode": "webhook",
					"url":  "http://localhost:1/sink",
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 8: Spec validation — malformed secret rejected ---
	demo.Step("What if I supply garbage instead of a `whsec_` value?").
		Arrow("Host", "Server", "events/subscribe { delivery: { secret: 'wrong' } }").
		DashedArrow("Server", "Host", "-32602 InvalidParams: delivery.secret invalid: must start with the whsec_ prefix").
		Note(
			"Rejected with `-32602 InvalidParams`. The validator enforces the full Standard Webhooks format: `whsec_` followed by base64 of 24-64 random bytes. A non-prefixed value, a too-short value, or non-base64 garbage all fail at subscribe time — catches IaC-pinned secrets that don't match the spec format before they create a broken subscription.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# A non-whsec_ value fails the Standard Webhooks format check → -32602 InvalidParams.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":11,"method":"events/subscribe","params":{"name":"discord.message","delivery":{"mode":"webhook","url":"http://localhost:1/sink","secret":"wrong"}}}' | jq '.error'`).Default(),
			demokit.MakeVariant("go", "go", `// Garbage secret (not whsec_<base64>) → server returns -32602 InvalidParams.
_, err := c.Call("events/subscribe", map[string]any{
    "name": "discord.message",
    "delivery": map[string]any{
        "mode":   "webhook",
        "url":    "http://localhost:1/sink",
        "secret": "wrong",
    },
})
rpcErr := err.(*client.RPCError) // code == -32602, must start with the whsec_ prefix`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": "wrong",
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 9: Spec validation — client-supplied id is rejected ---
	demo.Step("What if I try to pick my own subscription id?").
		Arrow("Host", "Server", "events/subscribe { id: 'mine', ... }").
		DashedArrow("Server", "Host", "-32602 InvalidParams: client-supplied id is not accepted").
		Note(
			"Rejected with `-32602 InvalidParams`. Per spec §\"Subscription Identity\" → \"Key composition\" L363, the id is server-derived from `(principal, name, params, url)` — there is no client-generated id. Old SDKs that send an `id` field get a loud error rather than a silent mis-keying that would alias subscriptions and break tenant isolation.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# The id is server-derived; a client-supplied id field is rejected → -32602 InvalidParams.
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":12,"method":"events/subscribe","params":{"id":"client-picked-id","name":"discord.message","delivery":{"mode":"webhook","url":"http://localhost:1/sink","secret":"whsec_<valid>"}}}' | jq '.error'`).Default(),
			demokit.MakeVariant("go", "go", `// Passing a client-chosen id → server returns -32602 InvalidParams.
_, err := c.Call("events/subscribe", map[string]any{
    "id":   "client-picked-id",
    "name": "discord.message",
    "delivery": map[string]any{
        "mode":   "webhook",
        "url":    "http://localhost:1/sink",
        "secret": events.GenerateSecret(),
    },
})
rpcErr := err.(*client.RPCError) // code == -32602, client-supplied id is not accepted`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			_, err := c.Call("events/subscribe", map[string]any{
				"id":   "client-picked-id",
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": events.GenerateSecret(),
				},
			})
			rpcErr, ok := err.(*client.RPCError)
			if !ok {
				fmt.Printf("    UNEXPECTED: want *client.RPCError, got %v\n", err)
				return
			}
			fmt.Printf("    code=%d  message=%q\n", rpcErr.Code, rpcErr.Message)
			if rpcErr.Code != -32602 {
				fmt.Printf("    UNEXPECTED: want code=-32602\n")
			}
			return
		})

	// --- Step 10: Spec validation — valid secret accepted, response omits secret ---
	demo.Step("And when everything is right?").
		Arrow("Host", "Host", "events.GenerateSecret() → whsec_<base64 of 32 bytes>").
		Arrow("Host", "Server", "events/subscribe { delivery: { secret: whsec_<valid> } }").
		DashedArrow("Server", "Host", "{ id: sub_<base64-of-16-bytes>, cursor, refreshBefore }   (no secret per spec)").
		Note(
			"Subscribe succeeds. The response carries the server-derived `id` (`sub_<base64>` per spec §\"Subscription Identity\" → \"Derived id\" L367), plus `cursor` and `refreshBefore`. Notably absent: the `secret` — the client supplied it, so the server doesn't echo it back. Echoing would risk leaks via proxies, logs, or IDE network panes.",
			"",
			"- The id is non-load-bearing for security; it's surfaced as `X-MCP-Subscription-Id` on delivery POSTs but knowing the value grants no operations on the subscription.",
		).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# A valid whsec_ secret succeeds: response carries the server-derived id, cursor,
# and refreshBefore — and notably NOT the secret (the server never echoes it).
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":13,"method":"events/subscribe","params":{"name":"discord.message","delivery":{"mode":"webhook","url":"http://localhost:1/sink","secret":"whsec_<valid>"}}}' | jq '.result'
# then tear down by tuple: events/unsubscribe { name, delivery: { url } }`).Default(),
			demokit.MakeVariant("go", "go", `// A conforming whsec_ secret succeeds; res.Raw has id + cursor + refreshBefore, no secret echo.
res, err := c.Call("events/subscribe", map[string]any{
    "name": "discord.message",
    "delivery": map[string]any{
        "mode":   "webhook",
        "url":    "http://localhost:1/sink",
        "secret": events.GenerateSecret(),
    },
})
if err != nil {
    log.Fatal(err)
}
_ = res
// Tear down by tuple (no id):
c.Call("events/unsubscribe", map[string]any{
    "name":     "discord.message",
    "delivery": map[string]any{"url": "http://localhost:1/sink"},
})`),
		).
		Run(func(_ demokit.StepContext) (result *demokit.StepResult) {
			supplied := events.GenerateSecret()
			fmt.Printf("    supplied secret:  %s...\n", truncate(supplied, 16))

			res, err := c.Call("events/subscribe", map[string]any{
				"name": "discord.message",
				"delivery": map[string]any{
					"mode":   "webhook",
					"url":    "http://localhost:1/sink",
					"secret": supplied,
				},
			})
			if err != nil {
				fmt.Printf("    UNEXPECTED: want success, got %v\n", err)
				return
			}
			var v any
			_ = json.Unmarshal(res.Raw, &v)
			pretty, _ := json.MarshalIndent(v, "    ", "  ")
			fmt.Printf("    events/subscribe response (server-derived id, no secret echo per spec):\n%s\n", string(pretty))

			// Eagerly unsubscribe (tuple form per spec
			// §"Unsubscribing: events/unsubscribe" L509, no id) so
			// the subscription doesn't tie up a delivery target
			// for a TTL window after the demo ends.
			_, _ = c.Call("events/unsubscribe", map[string]any{
				"name":     "discord.message",
				"delivery": map[string]any{"url": "http://localhost:1/sink"},
			})
			return
		})

	// --- Step 11: Live Discord interaction ---
	// Uses demokit's Timeout (safety upper bound) + Cancellable (press-enter
	// to end early in interactive non-TUI mode). Output streams live as
	// events arrive — demokit v0.0.8+ tees step stdout to the renderer in
	// real time rather than buffering until Run returns. Cancellable is
	// disabled in TUI mode because Bubble Tea owns stdin.
	demo.Step("Now let's see it against a real bot").
		Arrow("Discord", "Server", "TypingStart event (when you start typing in the channel)").
		DashedArrow("Server", "Host", "notifications/events/event { name: discord.typing, cursor: null }").
		Arrow("Discord", "Server", "MessageCreate event (when you press enter)").
		DashedArrow("Server", "Host", "notifications/events/event { name: discord.message, cursor: <fresh> }").
		Note(
			"Setup: start the server with a Discord bot token and invite the bot to a channel you can post in.",
			"",
			"```",
			"DISCORD_BOT_TOKEN=<your-token> make serve",
			"```",
			"",
			"Bot setup (token + invite URL) is documented in this demo's README.md.",
			"",
			"- TypingStart handler in main.go yields a cursorless discord.typing event; MessageCreate yields the cursored discord.message.",
			"- Discord's typing indicator fires once when you start (then refires every ~8s if you keep typing), not per keystroke.",
			"- --non-interactive mode skips the wait so CI runs aren't slowed.",
		).
		Timeout(liveInteractionMaxWait).
		Cancellable(!demokit.IsTUI()).
		VerbatimVariants("Reproduce on the wire",
			demokit.MakeVariant("curl", "bash", `# Hold two concurrent events/stream requests open against the same session,
# one per subscription (spec L271). Then type + send in the real Discord channel:
# discord.typing arrives with cursor:null, discord.message with a fresh cursor.
curl -N -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":14,"method":"events/stream","params":{"name":"discord.message"}}' &
curl -N -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: text/event-stream, application/json' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":15,"method":"events/stream","params":{"name":"discord.typing"}}' &`).Default(),
			demokit.MakeVariant("go", "go", `// Two concurrent events/stream calls on one session — one per subscription (spec L271).
msgStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
    EventName: "discord.message",
    OnEvent:   func(ev events.Event) { gotMsg <- ev }, // cursored
})
if err != nil {
    log.Fatalf("open discord.message stream: %v", err)
}
defer msgStream.Stop()
typingStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
    EventName: "discord.typing",
    OnEvent:   func(ev events.Event) { gotTyping <- ev }, // cursorless
})
if err != nil {
    log.Fatalf("open discord.typing stream: %v", err)
}
defer typingStream.Stop()`),
		).
		Run(func(ctx demokit.StepContext) (result *demokit.StepResult) {
			if demokit.IsNonInteractive() {
				fmt.Printf("    Skipped in --non-interactive mode. Run without --non-interactive (and with the server in -token mode) to see live events.\n")
				return
			}

			fmt.Printf("    Go to your Discord channel and start typing — typing indicators will show up here.\n")
			if demokit.IsTUI() {
				fmt.Printf("    Press Ctrl+C to exit when done. Will stop automatically after %s.\n\n", liveInteractionMaxWait)
			} else {
				fmt.Printf("    Press enter (here, in this terminal) when you're done capturing events.\n")
				fmt.Printf("    Will stop automatically after %s if you walk away.\n\n", liveInteractionMaxWait)
			}

			// Two concurrent events/stream calls against the same MCP
			// session — each is a separate POST holding its own SSE
			// response. requestId is the demux key on stdio; on
			// Streamable HTTP each stream's notifications already
			// arrive on its own response so per-call hooks naturally
			// isolate. Per spec L271: "A client MAY hold multiple
			// concurrent events/stream requests open, one per
			// subscription."
			liveCtx, liveCancel := context.WithCancel(ctx.Ctx)
			defer liveCancel()

			gotMsg := make(chan events.Event, 16)
			gotTyping := make(chan events.Event, 16)

			msgStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
				EventName: "discord.message",
				OnEvent:   func(ev events.Event) { gotMsg <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: open discord.message stream: %v\n", err)
				return
			}
			defer msgStream.Stop()

			typingStream, err := eventsclient.Stream(liveCtx, c, eventsclient.StreamOptions{
				EventName: "discord.typing",
				OnEvent:   func(ev events.Event) { gotTyping <- ev },
			})
			if err != nil {
				fmt.Printf("    ERROR: open discord.typing stream: %v\n", err)
				return
			}
			defer typingStream.Stop()

			var typingSeen, msgSeen int
			for {
				select {
				case ev := <-gotTyping:
					typingSeen++
					var d map[string]any
					_ = json.Unmarshal(ev.Data, &d)
					fmt.Printf("    [typing]  %v in channel %v\n", d["user"], d["channel_id"])
				case ev := <-gotMsg:
					msgSeen++
					var d map[string]any
					_ = json.Unmarshal(ev.Data, &d)
					fmt.Printf("    [message] %v: %q\n", d["author"], d["content"])
				case <-ctx.Ctx.Done():
					if typingSeen+msgSeen == 0 {
						fmt.Printf("    No live events received. Make sure the server is started with -token and the bot is invited to a channel you can post in.\n")
					} else {
						fmt.Printf("\n    Captured %d typing event(s) + %d message(s).\n", typingSeen, msgSeen)
					}
					return
				}
			}
		})

	demo.Section("Where each piece lives in mcpkit",
		"- Events library: `experimental/ext/events/`",
		"- Go client SDK (`Subscription`, `Receiver[Data]`): `experimental/ext/events/clients/go/`",
		"- Python client SDK (`WebhookSubscription` class + `webhook` CLI): `experimental/ext/events/clients/python/events_client.py`",
		"- Demo source: `examples/events/discord/`",
		"- Companion demo: `examples/events/telegram/` (lighter walkthrough — same protocol, different bot SDK)",
	)

	common.SetupRenderer(demo)

	demo.Execute()

	if c != nil {
		c.Close()
	}
}

// postInject is a tiny helper to POST a JSON body to /inject?event=<name>.
// Used by the steps that simulate an external event source.
func postInject(injectURL, eventName string, body map[string]any) error {
	raw, _ := json.Marshal(body)
	url := injectURL + "?event=" + eventName
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("inject returned %d", resp.StatusCode)
	}
	return nil
}

// truncate returns a short prefix for display.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

