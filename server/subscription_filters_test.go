package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

const (
	eventsListChangedMethod = "notifications/events/list_changed"
	tasksExtID              = "io.modelcontextprotocol/tasks"
)

// openListen POSTs subscriptions/listen with the given JSON-RPC id and
// notifications value. A nil notifications omits the field.
func openListen(t *testing.T, ctx context.Context, url string, id any, notifications any) *http.Response {
	t.Helper()
	return openListenWithCaps(t, ctx, url, id, notifications, map[string]any{})
}

func openListenWithCaps(t *testing.T, ctx context.Context, url string, id any, notifications any, caps map[string]any) *http.Response {
	t.Helper()
	params := map[string]any{
		"_meta": map[string]any{
			"io.modelcontextprotocol/protocolVersion":    draftVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "listen-test", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": caps,
		},
	}
	if notifications != nil {
		params["notifications"] = notifications
	}
	msg := map[string]any{"jsonrpc": "2.0", "method": "subscriptions/listen", "params": params}
	if id != nil {
		msg["id"] = id
	}
	body, _ := json.Marshal(msg)
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

func ackNotifications(t *testing.T, ack map[string]any) map[string]any {
	t.Helper()
	if ack["method"] != core.NotificationSubscriptionsAcknowledged {
		t.Fatalf("first frame = %v, want the acknowledgement", ack["method"])
	}
	params, _ := ack["params"].(map[string]any)
	n, ok := params["notifications"].(map[string]any)
	if !ok {
		t.Fatalf("acknowledgement has no params.notifications object: %+v", params)
	}
	return n
}

func frameSubID(f map[string]any) any {
	params, _ := f["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	return meta[core.MetaKeySubscriptionID]
}

// broadcastAfter fires each notification in order once the stream has had
// time to register.
func broadcastAfter(s *Server, notes ...[2]any) {
	go func() {
		time.Sleep(150 * time.Millisecond)
		for _, n := range notes {
			s.Broadcast(context.Background(), n[0].(string), n[1])
		}
	}()
}

func TestListen_AckEchoesOnlyHonoredKeys(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The server has tools and no prompts, and nobody registered bogusKey.
	resp := openListen(t, ctx, url, 7, map[string]any{
		"toolsListChanged":   true,
		"promptsListChanged": true,
		"bogusKey":           true,
	})
	defer resp.Body.Close()

	frames := readSSEFrames(t, resp, 1, time.Second)
	if len(frames) != 1 {
		t.Fatal("no acknowledgement")
	}
	got := ackNotifications(t, frames[0])
	if want := map[string]any{"toolsListChanged": true}; !mapsEqual(got, want) {
		t.Errorf("acknowledged notifications = %v, want %v", got, want)
	}
}

func TestListen_EmptyAckWhenNothingHonored(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp := openListen(t, ctx, url, 1, map[string]any{"toolsListChanged": false})
	defer resp.Body.Close()
	frames := readSSEFrames(t, resp, 1, time.Second)
	if len(frames) != 1 {
		t.Fatal("no acknowledgement")
	}
	if got := ackNotifications(t, frames[0]); len(got) != 0 {
		t.Errorf("acknowledged notifications = %v, want {}", got)
	}
}

func TestListen_ExtensionBooleanKey(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	if err := s.RegisterSubscriptionFilter(ListChangedFilterKey("eventsListChanged", eventsListChangedMethod)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	optedIn := openListen(t, ctx, url, 1, map[string]any{"eventsListChanged": true})
	defer optedIn.Body.Close()
	toolsOnly := openListen(t, ctx, url, 2, map[string]any{"toolsListChanged": true})
	defer toolsOnly.Body.Close()

	// Each stream's second frame shows what got through: the events
	// notification is broadcast first, so a tools-only stream that
	// wrongly admitted it would see it before the tools one.
	broadcastAfter(s,
		[2]any{eventsListChangedMethod, map[string]any{}},
		[2]any{"notifications/tools/list_changed", nil},
	)

	in := readSSEFrames(t, optedIn, 2, 3*time.Second)
	if len(in) != 2 {
		t.Fatalf("opted-in stream got %d frames, want ack + event", len(in))
	}
	if got := ackNotifications(t, in[0]); !mapsEqual(got, map[string]any{"eventsListChanged": true}) {
		t.Errorf("acknowledged notifications = %v, want eventsListChanged only", got)
	}
	if in[1]["method"] != eventsListChangedMethod {
		t.Errorf("opted-in stream second frame = %v, want %s", in[1]["method"], eventsListChangedMethod)
	}
	if frameSubID(in[1]) != float64(1) {
		t.Errorf("event frame subscriptionId = %v, want 1", frameSubID(in[1]))
	}

	out := readSSEFrames(t, toolsOnly, 2, 3*time.Second)
	if len(out) != 2 {
		t.Fatalf("tools-only stream got %d frames, want ack + tools", len(out))
	}
	if out[1]["method"] != "notifications/tools/list_changed" {
		t.Errorf("tools-only stream second frame = %v, want tools/list_changed (events leaked?)", out[1]["method"])
	}
}

// taskIDsKey is shaped like ext-tasks' taskIds filter: an array of IDs,
// narrowed per caller at accept time, matched on the notification's
// params.
func taskIDsKey(visible map[string]bool, sawClient *string) SubscriptionFilterKey {
	return SubscriptionFilterKey{
		Key: "taskIds",
		Accept: func(ctx context.Context, requested json.RawMessage) (*SubscriptionFilterGrant, error) {
			m := core.RequestMetaFromContext(ctx)
			if m != nil && m.ClientInfo != nil {
				*sawClient = m.ClientInfo.Name
			}
			declared := false
			if m != nil && m.ClientCapabilities != nil {
				_, declared = m.ClientCapabilities.Extensions[tasksExtID]
			}
			if !declared {
				return nil, &core.MissingCapabilityError{
					Required: core.ClientCapabilities{Extensions: map[string]core.ClientExtensionCap{tasksExtID: {}}},
					Message:  "taskIds needs the tasks extension",
				}
			}
			var ids []string
			if json.Unmarshal(requested, &ids) != nil {
				return nil, nil
			}
			var accepted []string
			for _, id := range ids {
				if visible[id] {
					accepted = append(accepted, id)
				}
			}
			if len(accepted) == 0 {
				return nil, nil
			}
			return &SubscriptionFilterGrant{
				Echo: accepted,
				Match: func(method string, params json.RawMessage) bool {
					var p struct {
						TaskID string `json:"taskId"`
					}
					return method == "notifications/tasks/status" &&
						json.Unmarshal(params, &p) == nil && slices.Contains(accepted, p.TaskID)
				},
			}, nil
		},
	}
}

type taskStatusParams struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

func TestListen_ArrayKeyNarrowedAndMatchedOnParams(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	var sawClient string
	if err := s.RegisterSubscriptionFilter(taskIDsKey(map[string]bool{"t1": true, "t2": true}, &sawClient)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// t3 is not visible to this caller, so it is dropped from the echo.
	resp := openListenWithCaps(t, ctx, url, "listen-a", map[string]any{"taskIds": []string{"t1", "t3"}}, tasksCaps)
	defer resp.Body.Close()

	broadcastAfter(s,
		[2]any{"notifications/tasks/status", taskStatusParams{TaskID: "t2", Status: "working"}},
		[2]any{"notifications/tasks/status", taskStatusParams{TaskID: "t1", Status: "completed"}},
	)

	frames := readSSEFrames(t, resp, 2, 3*time.Second)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want ack + t1 status", len(frames))
	}
	if got := ackNotifications(t, frames[0]); !mapsEqual(got, map[string]any{"taskIds": []any{"t1"}}) {
		t.Errorf("acknowledged notifications = %v, want taskIds [t1]", got)
	}
	if sawClient != "listen-test" {
		t.Errorf("Accept saw clientInfo.name %q, want the request's _meta", sawClient)
	}
	if got := frameSubID(frames[0]); got != "listen-a" {
		t.Errorf("ack subscriptionId = %v, want the string id listen-a", got)
	}
	params, _ := frames[1]["params"].(map[string]any)
	if params["taskId"] != "t1" || params["status"] != "completed" {
		t.Errorf("status frame params = %v, want taskId t1 completed at top level (t2 leaked, or struct params nested)", params)
	}
	if frameSubID(frames[1]) != "listen-a" {
		t.Errorf("status frame subscriptionId = %v, want listen-a", frameSubID(frames[1]))
	}
}

var tasksCaps = map[string]any{"extensions": map[string]any{tasksExtID: map[string]any{}}}

// An Accept that returns *core.MissingCapabilityError refuses the whole
// listen with -32021, the code ext-tasks names for taskIds from a client
// that did not declare the extension.
func TestListen_AcceptErrorRefusesWithMinus32021(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	var sawClient string
	if err := s.RegisterSubscriptionFilter(taskIDsKey(map[string]bool{"t1": true}, &sawClient)); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp := openListen(t, ctx, url, 1, map[string]any{"taskIds": []string{"t1"}, "toolsListChanged": true})
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var r core.Response
	if err := json.Unmarshal(raw, &r); err != nil || r.Error == nil {
		t.Fatalf("want a JSON-RPC error, got %s", raw)
	}
	if r.Error.Code != core.ErrCodeMissingRequiredClientCapability {
		t.Errorf("code = %d, want %d", r.Error.Code, core.ErrCodeMissingRequiredClientCapability)
	}
	if !bytes.Contains(raw, []byte(tasksExtID)) {
		t.Errorf("error.data does not name the required extension: %s", raw)
	}
}

// Two clients may both use JSON-RPC id 1. Each stream must keep its own
// filter and its own frames.
func TestListen_SameRequestIDOnTwoStreams(t *testing.T) {
	s, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	if err := s.RegisterSubscriptionFilter(ListChangedFilterKey("eventsListChanged", eventsListChangedMethod)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := openListen(t, ctx, url, 1, map[string]any{"toolsListChanged": true})
	defer a.Body.Close()
	b := openListen(t, ctx, url, 1, map[string]any{"eventsListChanged": true})
	defer b.Body.Close()

	broadcastAfter(s,
		[2]any{"notifications/tools/list_changed", nil},
		[2]any{eventsListChangedMethod, map[string]any{}},
	)

	fa := readSSEFrames(t, a, 2, 3*time.Second)
	fb := readSSEFrames(t, b, 2, 3*time.Second)
	if len(fa) != 2 || fa[1]["method"] != "notifications/tools/list_changed" {
		t.Errorf("stream A frames = %v, want ack + tools/list_changed", fa)
	}
	if len(fb) != 2 || fb[1]["method"] != eventsListChangedMethod {
		t.Errorf("stream B frames = %v, want ack + events/list_changed", fb)
	}
}

func TestListen_RejectsMissingNotificationsAndID(t *testing.T) {
	_, url, teardown := newStatelessTestServer(t, stateless.ModeDual)
	defer teardown()
	// A server that wrongly accepts the request opens a stream that never
	// ends, so the read needs a deadline to fail rather than hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for name, tc := range map[string]struct {
		id            any
		notifications any
		code          int
	}{
		"missing notifications": {id: 1, notifications: nil, code: core.ErrCodeInvalidParams},
		"missing id":            {id: nil, notifications: map[string]any{"toolsListChanged": true}, code: core.ErrCodeInvalidRequest},
	} {
		t.Run(name, func(t *testing.T) {
			resp := openListen(t, ctx, url, tc.id, tc.notifications)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var r core.Response
			if err := json.Unmarshal(raw, &r); err != nil || r.Error == nil {
				t.Fatalf("want a JSON-RPC error, got %s", raw)
			}
			if r.Error.Code != tc.code {
				t.Errorf("code = %d, want %d", r.Error.Code, tc.code)
			}
		})
	}
}

func TestRegisterSubscriptionFilter_Conflicts(t *testing.T) {
	s := NewServer(core.ServerInfo{Name: "t", Version: "1"})
	k := ListChangedFilterKey("agentsListChanged", "notifications/agents/list_changed")
	if err := s.RegisterSubscriptionFilter(k); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := s.RegisterSubscriptionFilter(k); !errors.Is(err, ErrSubscriptionFilterKeyTaken) {
		t.Errorf("duplicate: err = %v, want ErrSubscriptionFilterKeyTaken", err)
	}
	base := ListChangedFilterKey(core.SubscriptionFilterToolsListChanged, "x")
	if err := s.RegisterSubscriptionFilter(base); !errors.Is(err, ErrSubscriptionFilterKeyTaken) {
		t.Errorf("base key: err = %v, want ErrSubscriptionFilterKeyTaken", err)
	}
	if err := s.RegisterSubscriptionFilter(SubscriptionFilterKey{Key: "x"}); err == nil {
		t.Error("a key with no Accept func registered without error")
	}
}

func TestNewTaggedFrame_KeepsParamsAndMeta(t *testing.T) {
	id := json.RawMessage(`"sub-a"`)

	f := stateless.NewTaggedFrame("n", map[string]any{
		"_meta": map[string]any{"traceparent": "00-abc-def-01"},
		"x":     1,
	}, id)
	meta := f.Params["_meta"].(map[string]any)
	if meta["traceparent"] != "00-abc-def-01" || string(meta[core.MetaKeySubscriptionID].(json.RawMessage)) != `"sub-a"` {
		t.Errorf("_meta = %v, want traceparent kept and subscriptionId added", meta)
	}
	if f.Params["x"] != 1 {
		t.Errorf("params = %v, want x kept", f.Params)
	}

	f = stateless.NewTaggedFrame("n", taskStatusParams{TaskID: "t1", Status: "working"}, id)
	if f.Params["taskId"] != "t1" || f.Params["data"] != nil {
		t.Errorf("struct params = %v, want fields at top level", f.Params)
	}
}

func mapsEqual(a, b map[string]any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return bytes.Equal(ra, rb)
}
