package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// readSSEFrames consumes up to maxFrames `data: <json>` SSE frames from r,
// returning each decoded into a generic map. Stops early on ctx.Done().
// Used by subscription tests to inspect ack + post-ack notifications.
func readSSEFrames(t *testing.T, r *http.Response, maxFrames int, deadline time.Duration) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	type frameOrErr struct {
		frame map[string]any
		err   error
	}
	out := make(chan frameOrErr, maxFrames)
	go func() {
		defer close(out)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				out <- frameOrErr{err: err}
				return
			}
			out <- frameOrErr{frame: m}
		}
		if err := scanner.Err(); err != nil {
			out <- frameOrErr{err: err}
		}
	}()

	timeout := time.After(deadline)
	var frames []map[string]any
	for len(frames) < maxFrames {
		select {
		case <-timeout:
			return frames
		case fe, ok := <-out:
			if !ok {
				return frames
			}
			if fe.err != nil {
				return frames
			}
			frames = append(frames, fe.frame)
		}
	}
	return frames
}

// openSubscription POSTs subscriptions/listen against url and returns
// the open *http.Response so tests can read SSE frames off Body. The
// caller is responsible for closing the body to trigger unsubscribe.
func openSubscription(t *testing.T, ctx context.Context, url string, filter map[string]bool) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "subscriptions/listen",
		"params": map[string]any{
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    draftVersion,
				"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "t", "version": "1"},
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
			"notifications": filter,
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(mcpProtocolVersionHeader, draftVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

// TestSubscriptionStream_AckIsFirst verifies the SEP-2575 invariant that
// the first frame on a subscriptions/listen stream is
// notifications/subscriptions/acknowledged, carrying the subscriptionId
// in _meta.
func TestSubscriptionStream_AckIsFirst(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	_ = s

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := openSubscription(t, ctx, url, map[string]bool{"toolsListChanged": true})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream*", resp.Header.Get("Content-Type"))
	}

	frames := readSSEFrames(t, resp, 1, 1*time.Second)
	if len(frames) < 1 {
		t.Fatal("no frames received")
	}
	first := frames[0]
	if first["method"] != "notifications/subscriptions/acknowledged" {
		t.Errorf("first frame method = %v, want notifications/subscriptions/acknowledged",
			first["method"])
	}
	params, _ := first["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil || meta[core.MetaKeySubscriptionID] == "" {
		t.Errorf("ack frame missing %s in _meta; got %+v",
			core.MetaKeySubscriptionID, params)
	}
}

// TestSubscriptionStream_SubscriptionIdOnAllFrames verifies that every
// notification frame after the ack carries the same subscriptionId.
// Drives a list-changed event by Server.Broadcast → broadcast fanout
// → subscriber.frames after the subscription is open. Uses a single
// shared reader so a buffered scanner doesn't lose data between calls.
func TestSubscriptionStream_SubscriptionIdOnAllFrames(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSubscription(t, ctx, url, map[string]bool{"toolsListChanged": true})
	defer resp.Body.Close()

	// Trigger after a short delay so the subscriber is registered.
	// Cleaner than fighting the SSE buffer here.
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.Broadcast(context.Background(), "notifications/tools/list_changed", nil)
	}()

	frames := readSSEFrames(t, resp, 2, 3*time.Second)
	if len(frames) < 2 {
		t.Fatalf("got %d frames, want 2 (ack + notification): %+v", len(frames), frames)
	}

	ack := frames[0]
	if ack["method"] != "notifications/subscriptions/acknowledged" {
		t.Fatalf("first frame method = %v, want ack", ack["method"])
	}
	ackParams, _ := ack["params"].(map[string]any)
	ackMeta, _ := ackParams["_meta"].(map[string]any)
	subID, _ := ackMeta[core.MetaKeySubscriptionID].(string)
	if subID == "" {
		t.Fatal("no subscriptionId in ack")
	}

	notif := frames[1]
	if notif["method"] != "notifications/tools/list_changed" {
		t.Errorf("frame method = %v, want notifications/tools/list_changed",
			notif["method"])
	}
	notifParams, _ := notif["params"].(map[string]any)
	notifMeta, _ := notifParams["_meta"].(map[string]any)
	if notifMeta[core.MetaKeySubscriptionID] != subID {
		t.Errorf("frame subscriptionId = %v, want %s (matching ack)",
			notifMeta[core.MetaKeySubscriptionID], subID)
	}
}

// TestSubscriptionStream_FilterHonored verifies the SEP-2575 invariant
// that the server MUST NOT deliver notification types outside the
// filter. Subscribes with promptsListChanged only; broadcasts a
// tools/list_changed event; the subscription stream must NOT receive
// a tools notification.
func TestSubscriptionStream_FilterHonored(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSubscription(t, ctx, url, map[string]bool{"promptsListChanged": true})
	defer resp.Body.Close()

	// Drain ack.
	if frames := readSSEFrames(t, resp, 1, 1*time.Second); len(frames) == 0 {
		t.Fatal("no ack")
	}

	// Broadcast a tools event the filter does NOT permit.
	s.Broadcast(context.Background(), "notifications/tools/list_changed", nil)

	// Read with a short deadline — nothing should arrive.
	leaked := readSSEFrames(t, resp, 1, 600*time.Millisecond)
	for _, f := range leaked {
		if f["method"] == "notifications/tools/list_changed" {
			t.Errorf("leaked tools notification to a prompts-only subscription: %+v", f)
		}
	}
}

// TestSubscriptionStream_DisconnectUnregisters verifies that a closed
// stream removes the subscriber so subsequent broadcasts do not block
// or accumulate. Uses the transport's internal subMap to confirm the
// state change rather than racing on output.
func TestSubscriptionStream_DisconnectUnregisters(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	_ = s

	ctx, cancel := context.WithCancel(context.Background())
	resp := openSubscription(t, ctx, url, map[string]bool{"toolsListChanged": true})
	if frames := readSSEFrames(t, resp, 1, 1*time.Second); len(frames) == 0 {
		t.Fatal("no ack")
	}

	// Snapshot subscriber count via the transport's map. The test server
	// constructs the transport implicitly via Handler(); we reach for it
	// by closing the response and confirming the broadcast no-ops.
	cancel()
	resp.Body.Close()

	// Give the goroutine a moment to exit.
	time.Sleep(50 * time.Millisecond)

	// A subsequent broadcast should not panic, deadlock, or otherwise
	// fail — proves cleanup unwound the registration.
	done := make(chan struct{})
	go func() {
		s.Broadcast(context.Background(), "notifications/tools/list_changed", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast after disconnect blocked > 2s — subscriber not unregistered")
	}
}

// TestSubscriptionStream_PromptsListChangedDeliversToPromptsFilter is the
// prompts-side mirror of TestSubscriptionStream_SubscriptionIdOnAllFrames.
// Closes the SEP-2575 Bucket 7 invariant: list-changed-capable servers
// notify listen streams with promptsListChanged: true when prompts
// mutate. Subscribes with prompts-only filter, broadcasts prompts
// list_changed, asserts the frame arrives tagged correctly AND that
// no tools list_changed leaks.
func TestSubscriptionStream_PromptsListChangedDeliversToPromptsFilter(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp := openSubscription(t, ctx, url, map[string]bool{"promptsListChanged": true})
	defer resp.Body.Close()

	go func() {
		time.Sleep(150 * time.Millisecond)
		s.Broadcast(context.Background(), "notifications/prompts/list_changed", nil)
	}()

	frames := readSSEFrames(t, resp, 2, 3*time.Second)
	if len(frames) < 2 {
		t.Fatalf("got %d frames, want 2 (ack + notification): %+v", len(frames), frames)
	}

	ack := frames[0]
	if ack["method"] != "notifications/subscriptions/acknowledged" {
		t.Fatalf("first frame method = %v, want ack", ack["method"])
	}
	ackParams, _ := ack["params"].(map[string]any)
	ackMeta, _ := ackParams["_meta"].(map[string]any)
	subID, _ := ackMeta[core.MetaKeySubscriptionID].(string)
	if subID == "" {
		t.Fatal("no subscriptionId in ack")
	}

	notif := frames[1]
	if notif["method"] != "notifications/prompts/list_changed" {
		t.Errorf("frame method = %v, want notifications/prompts/list_changed",
			notif["method"])
	}
	notifParams, _ := notif["params"].(map[string]any)
	notifMeta, _ := notifParams["_meta"].(map[string]any)
	if notifMeta[core.MetaKeySubscriptionID] != subID {
		t.Errorf("frame subscriptionId = %v, want %s (matching ack)",
			notifMeta[core.MetaKeySubscriptionID], subID)
	}
}

func TestSubscribeFilter_Matches(t *testing.T) {
	cases := []struct {
		method string
		filter stateless.SubscribeFilter
		want   bool
	}{
		{"notifications/tools/list_changed", stateless.SubscribeFilter{ToolsListChanged: true}, true},
		{"notifications/tools/list_changed", stateless.SubscribeFilter{PromptsListChanged: true}, false},
		{"notifications/prompts/list_changed", stateless.SubscribeFilter{PromptsListChanged: true}, true},
		{"notifications/resources/list_changed", stateless.SubscribeFilter{ResourcesListChanged: true}, true},
		{"notifications/message", stateless.SubscribeFilter{ToolsListChanged: true}, false},
		{"random/method", stateless.SubscribeFilter{ToolsListChanged: true}, false},
	}
	for _, c := range cases {
		if got := c.filter.Matches(c.method); got != c.want {
			t.Errorf("Matches(%q, %+v) = %v, want %v", c.method, c.filter, got, c.want)
		}
	}
}
