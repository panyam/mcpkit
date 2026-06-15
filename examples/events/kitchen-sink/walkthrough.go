package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/examples/common"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
)

// nopReceiverServer returns an httptest server that 200-OKs every
// webhook delivery without signature verification. Used by the quota
// step which only cares whether subscribe was accepted, not whether
// deliveries arrive.
func nopReceiverServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

const liveCaptureWindow = 8 * time.Second

func runDemo() {
	serverURL := common.ServerURL()
	mcpURL := serverURL + "/mcp"
	injectURL := serverURL + "/inject"

	demo := demokit.New("MCP Events — kitchen-sink (per-subscription showcase)").
		Dir("events/kitchen-sink").
		Description("Single-process showcase of the per-subscription delivery surface added by the η work. Three event sources fan out to several distinct subscribers per source with different params, so the spec's per-subscription Match / Transform / OnSubscribe + EmitToSubscription model is actually visible on the wire instead of being theoretical. Companion to examples/whole-enchilada/events/ which exercises the deploy axis.").
		Actors(
			demokit.Actor("Host", "MCP Host (this walkthrough)"),
			demokit.Actor("Server", "MCP Events server (make serve)"),
			demokit.Actor("SubA", "Subscriber A — chat channel:general"),
			demokit.Actor("SubB", "Subscriber B — chat channel:dev"),
			demokit.Actor("SubC", "Subscriber C — alert.fired (redact_pii:true)"),
			demokit.Actor("SubD", "Subscriber D — alert.fired (redact_pii:false)"),
			demokit.Actor("SubE", "Subscriber E — presence watch_users=[alice]"),
			demokit.Actor("SubF", "Subscriber F — presence watch_users=[bob]"),
		)

	demo.Section("What this demo covers",
		"- **Match by params** — two subs to chat.message, different `channel` params; only matching events delivered.",
		"- **Transform by params** — two subs to alert.fired, one with `redact_pii:true`; same upstream event, different bytes on the wire per subscriber.",
		"- **OnSubscribe + EmitToSubscription** — presence.changed uses targeted emit driven by per-subscription watch lists captured at subscribe time; Match / Transform are NOT invoked on this path.",
		"- **Quota cap rejection** — chat.message capped at 2 subscriptions per principal; the third subscribe returns `-32013 TooManySubscriptions` with the spec wire shape.",
		"- **Cursorless source on the wire** — presence emits `cursor:null` and Poll always returns empty.",
		"",
		"Single-process; in-memory; synthetic upstreams. Two-terminal flow:",
		"",
		"```",
		"Terminal 1:  make serve            # the events server",
		"Terminal 2:  make demo             # this walkthrough (--tui)",
		"             make demo-test        # non-interactive run",
		"             make readme           # regenerate WALKTHROUGH.md",
		"```",
	)

	var c *client.Client

	demo.Step("How do I open the conversation?").
		Arrow("Host", "Server", "POST /mcp — initialize").
		DashedArrow("Server", "Host", "serverInfo + capabilities").
		Note("Vanilla MCP initialize. The events extension declares no new capability; events/* methods are registered server-side via the library.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			c = client.NewClient(mcpURL, core.ClientInfo{Name: "kitchen-sink-host", Version: "1.0"})
			if err := c.Connect(); err != nil {
				fmt.Printf("    ERROR: %v\n    Start the server with: make serve\n", err)
			} else {
				fmt.Printf("    Connected to %s %s\n", c.ServerInfo.Name, c.ServerInfo.Version)
			}
			return nil
		})

	demo.Step("Which events does this server publish?").
		Arrow("Host", "Server", "events/list").
		DashedArrow("Server", "Host", "[chat.message (cursored), alert.fired (cursored), presence.changed (cursorless)]").
		Note("Three sources. chat.message + alert.fired drive the Match / Transform stories on broadcast emit; presence.changed drives the OnSubscribe + EmitToSubscription story on targeted emit.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			raw, err := c.Call("events/list", nil)
			if err != nil {
				fmt.Printf("    events/list error: %v\n", err)
				return nil
			}
			pretty, _ := json.MarshalIndent(json.RawMessage(raw.Raw), "    ", "  ")
			fmt.Printf("    %s\n", pretty)
			return nil
		})

	demo.Step("Two subs to chat.message, different channel params — how does Match route them?").
		Arrow("SubA", "Server", "events/stream{name:chat.message, params:{channel:general}}").
		Arrow("SubB", "Server", "events/stream{name:chat.message, params:{channel:dev}}").
		DashedArrow("Server", "SubA", "notifications/events/event (channel:general only)").
		DashedArrow("Server", "SubB", "notifications/events/event (channel:dev only)").
		Note("EventDef.Match is invoked per (event × subscriber) pair on broadcast emit. Returns true → the event reaches this subscriber; false → the library drops it. Empty params match everything (spec default).").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			subA, gotA := openStream(c, "chat.message", map[string]any{"channel": "general"})
			subB, gotB := openStream(c, "chat.message", map[string]any{"channel": "dev"})
			defer subA.Stop()
			defer subB.Stop()

			// Inject one event per channel + one we don't expect either sub
			// to receive (channel:alerts).
			_ = postInject(injectURL, "chat.message", map[string]any{"channel": "general", "sender": "alice", "text": "hi general"})
			_ = postInject(injectURL, "chat.message", map[string]any{"channel": "dev", "sender": "bob", "text": "hi dev"})
			_ = postInject(injectURL, "chat.message", map[string]any{"channel": "alerts", "sender": "carol", "text": "noise"})

			waitFor := time.After(2 * time.Second)
			var aCount, bCount int
		loop:
			for {
				select {
				case ev := <-gotA:
					aCount++
					fmt.Printf("    SubA received channel=%s text=%q\n", chatChannelFor(ev), chatTextFor(ev))
				case ev := <-gotB:
					bCount++
					fmt.Printf("    SubB received channel=%s text=%q\n", chatChannelFor(ev), chatTextFor(ev))
				case <-waitFor:
					break loop
				}
			}
			fmt.Printf("    summary: SubA=%d events SubB=%d events (alerts channel should reach neither)\n", aCount, bCount)
			return nil
		})

	demo.Step("Two subs to alert.fired — only one is allowed to see PII. Same upstream event, different bytes per sub. How?").
		Arrow("SubC", "Server", "events/stream{name:alert.fired, params:{severity:P1, redact_pii:true}}").
		Arrow("SubD", "Server", "events/stream{name:alert.fired, params:{severity:P1, redact_pii:false}}").
		DashedArrow("Server", "SubC", "notifications/events/event (reporter cleared, email redacted)").
		DashedArrow("Server", "SubD", "notifications/events/event (raw)").
		Note("EventDef.Transform runs per subscriber after Match. Returning (event, true) installs the modified bytes for this sub; returning (event, false) leaves the original. Subscribers that don't opt in see the unredacted payload; opted-in subs see the cleared reporter and the `<redacted-email>` placeholder.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			subC, gotC := openStream(c, "alert.fired", map[string]any{"severity": "P1", "redact_pii": true})
			subD, gotD := openStream(c, "alert.fired", map[string]any{"severity": "P1", "redact_pii": false})
			defer subC.Stop()
			defer subD.Stop()

			_ = postInject(injectURL, "alert.fired", map[string]any{
				"severity": "P1", "service": "api-gateway",
				"reporter": "alice", "message": "latency spike — page alice@example.com",
			})

			waitFor := time.After(2 * time.Second)
			seenC, seenD := false, false
		loop:
			for {
				select {
				case ev := <-gotC:
					seenC = true
					fmt.Printf("    SubC (redacted): %s\n", string(ev.Data))
				case ev := <-gotD:
					seenD = true
					fmt.Printf("    SubD (raw):     %s\n", string(ev.Data))
				case <-waitFor:
					break loop
				}
				if seenC && seenD {
					break loop
				}
			}
			return nil
		})

	demo.Step("Each user only wants to watch *their* friends' presence. Broadcasting and filtering would waste frames. What's the targeted-emit pattern?").
		Arrow("SubE", "Server", "events/stream{name:presence.changed, params:{watch_users:[alice]}}").
		Arrow("SubF", "Server", "events/stream{name:presence.changed, params:{watch_users:[bob]}}").
		Note("EventDef.OnSubscribe captures the watch lists in a per-sub registry keyed by subscriptionID. The presence feeder then uses events.EmitToSubscription(idx, ev, subID) to deliver each transition straight to the matching subs — Match and Transform are NOT invoked on this path. The library does no broadcast for presence at all; the routing is fully resolved at emit time by the author code.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			subE, gotE := openStream(c, "presence.changed", map[string]any{"watch_users": []any{"alice"}})
			subF, gotF := openStream(c, "presence.changed", map[string]any{"watch_users": []any{"bob"}})
			defer subE.Stop()
			defer subF.Stop()
			time.Sleep(150 * time.Millisecond)

			_ = postInject(injectURL, "presence.changed", map[string]any{"user": "alice", "state": "online"})
			_ = postInject(injectURL, "presence.changed", map[string]any{"user": "bob", "state": "away"})
			_ = postInject(injectURL, "presence.changed", map[string]any{"user": "carol", "state": "offline"})

			waitFor := time.After(2 * time.Second)
			var eHits, fHits int
		loop:
			for {
				select {
				case ev := <-gotE:
					eHits++
					fmt.Printf("    SubE received %s\n", string(ev.Data))
				case ev := <-gotF:
					fHits++
					fmt.Printf("    SubF received %s\n", string(ev.Data))
				case <-waitFor:
					break loop
				}
			}
			fmt.Printf("    summary: SubE=%d events (alice only) SubF=%d events (bob only); carol reached nobody\n", eHits, fHits)
			return nil
		})

	demo.Step("What stops a noisy client from subscribing 100 times?").
		Arrow("Host", "Server", "events/subscribe ×3 (chat.message)").
		DashedArrow("Server", "Host", "1st: id=sub_...; 2nd: id=sub_...; 3rd: error -32013").
		Note("Quota is configured per-principal-per-event-type at events.Register time. The third subscribe under the same principal returns -32013 ResourceExhausted with structured data {limit:\"subscriptions\", max:N} — clients read the max to know the cap without consulting docs. " +
			"Two emission paths share this shape (see experimental/ext/events/errors.go's ResourceExhaustedData godoc for the canonical table): " +
			"(a) Reserve failure — quota counter at the configured cap; message names the cap; data.max > 0. " +
			"(b) on_subscribe rejection — author OnSubscribe hook returned an error AFTER Reserve granted; message starts \"on_subscribe rejected:\"; data.max absent on the wire (the server didn't impose this rejection, the author did). " +
			"Clients that want to discriminate read data.max presence; clients that just want \"too many subscriptions\" UX treat both uniformly. " +
			"This shape is the canonical reference for whole-enchilada stages 2/3/4 (tenant-level quotas, multi-replica): same code, same data shape, same two emission paths.").
		Run(func(_ demokit.StepContext) *demokit.StepResult {
			if c == nil {
				return nil
			}
			var lastErr error
			for i := 1; i <= 3; i++ {
				recv := nopReceiverServer()
				_, err := eventsclient.Subscribe(context.Background(), c, eventsclient.SubscribeOptions{
					EventName:   "chat.message",
					CallbackURL: recv.URL,
				})
				if err != nil {
					fmt.Printf("    attempt %d failed: %v\n", i, err)
					lastErr = err
					recv.Close()
					break
				}
				fmt.Printf("    attempt %d succeeded\n", i)
			}
			if lastErr != nil {
				common.PrintRPCError(lastErr, "")
			}
			return nil
		})

	demo.Section("Where each piece lives in mcpkit",
		"- **Hook API** — `experimental/ext/events/hooks.go` defines MatchFunc / TransformFunc / SubscribeFunc / UnsubscribeFunc and the HookContext.",
		"- **EventDef hook fields** — `experimental/ext/events/events.go` (`EventDef.Match` / `Transform` / `OnSubscribe` / `OnUnsubscribe`).",
		"- **Targeted emit** — `experimental/ext/events/emit_targeted.go`.",
		"- **Quota wire shape** — `experimental/ext/events/quota.go`.",
		"- **Companion demos** — `examples/events/discord/` (one source, real bot), `examples/events/telegram/` (one source, real bot), `examples/whole-enchilada/events/` (multi-tier deploy, synthetic upstreams).",
	)

	demo.Execute()
	if c != nil {
		_ = c.Close()
	}
}

// openStream returns the Stream call + a channel that the OnEvent
// callback forwards events onto. Used by every per-sub demo step.
func openStream(c *client.Client, eventName string, params map[string]any) (*eventsclient.StreamCall, <-chan events.Event) {
	ch := make(chan events.Event, 8)
	stream, err := eventsclient.Stream(context.Background(), c, eventsclient.StreamOptions{
		EventName: eventName,
		Arguments: params,
		OnEvent:   func(ev events.Event) { ch <- ev },
	})
	if err != nil {
		fmt.Printf("    events/stream error for params=%v: %v\n", params, err)
		// Return a no-op stream so callers can still .Stop().
		close(ch)
		return &eventsclient.StreamCall{}, ch
	}
	return stream, ch
}

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

func chatChannelFor(ev events.Event) string {
	var d ChatMessageData
	_ = json.Unmarshal(ev.Data, &d)
	return d.Channel
}

func chatTextFor(ev events.Event) string {
	var d ChatMessageData
	_ = json.Unmarshal(ev.Data, &d)
	return strings.TrimSpace(d.Text)
}
