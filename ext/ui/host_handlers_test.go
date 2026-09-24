package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// startHost starts an AppHost with a nil client, so any request that reaches
// the forward path panics. Host-capability methods must never get there.
func startHost(t *testing.T, opts ...AppHostOption) *InProcessAppBridge {
	t.Helper()
	bridge := NewInProcessAppBridge()
	host := NewAppHost(nil, bridge, opts...)
	if err := host.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	return bridge
}

// TestHostHandlers_UpdateModelContext verifies ui/update-model-context is
// answered by the host handler with the spec's params shape and an empty result.
func TestHostHandlers_UpdateModelContext(t *testing.T) {
	var got ModelContextUpdate
	bridge := startHost(t, WithHostHandlers(HostHandlers{
		UpdateModelContext: func(_ context.Context, u ModelContextUpdate) error {
			got = u
			return nil
		},
	}))

	resp, err := bridge.SendToHost(context.Background(), MethodUpdateModelContext, map[string]any{
		"content":           []map[string]any{{"type": "text", "text": "map centred on Detroit"}},
		"structuredContent": map[string]any{"selected": 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if len(got.Content) != 1 || got.Content[0].Text != "map centred on Detroit" {
		t.Errorf("content = %+v", got.Content)
	}
	if got.StructuredContent["selected"] != float64(3) {
		t.Errorf("structuredContent = %+v", got.StructuredContent)
	}
	raw, _ := ToBytes(resp.Result)
	if string(raw) != "{}" {
		t.Errorf("result = %s, want {}", raw)
	}
}

// TestHostHandlers_NotInstalled verifies an unhandled host method gets -32601
// instead of being forwarded to the MCP server. ui/request-display-mode is the
// exception, covered by TestHostHandlers_DisplayModeDefault.
func TestHostHandlers_NotInstalled(t *testing.T) {
	bridge := startHost(t)
	for _, m := range []string{MethodOpenLink, MethodDownloadFile, MethodMessage, MethodUpdateModelContext} {
		resp, err := bridge.SendToHost(context.Background(), m, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != core.ErrCodeMethodNotFound {
			t.Errorf("%s: error = %+v, want -32601", m, resp.Error)
		}
	}
}

// TestHostHandlers_DisplayModeDefault covers the spec rule that the host MUST
// answer ui/request-display-mode with the mode actually in effect. A host with
// no RequestDisplayMode handler never changes modes, so the answer is inline.
func TestHostHandlers_DisplayModeDefault(t *testing.T) {
	bridge := startHost(t)
	resp, err := bridge.SendToHost(context.Background(), MethodRequestDisplayMode, map[string]any{"mode": "fullscreen"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("error = %+v, want a result", resp.Error)
	}
	raw, _ := ToBytes(resp.Result)
	if string(raw) != `{"mode":"inline"}` {
		t.Errorf("result = %s, want {\"mode\":\"inline\"}", raw)
	}
}

// TestHostHandlers_Errors verifies a HostError keeps its code, a plain error
// becomes -32000, and malformed params are -32602.
func TestHostHandlers_Errors(t *testing.T) {
	bridge := startHost(t, WithHostHandlers(HostHandlers{
		OpenLink: func(_ context.Context, r OpenLinkRequest) error {
			if r.URL == "" {
				return &HostError{Code: core.ErrCodeInvalidParams, Message: "Invalid URL"}
			}
			return errors.New("Link opening denied by user")
		},
	}))

	cases := []struct {
		params any
		code   int
	}{
		{map[string]any{"url": ""}, core.ErrCodeInvalidParams},
		{map[string]any{"url": "https://example.com"}, core.ErrCodeServerError},
		{map[string]any{"url": 42}, core.ErrCodeInvalidParams},
	}
	for _, c := range cases {
		resp, _ := bridge.SendToHost(context.Background(), MethodOpenLink, c.params)
		if resp.Error == nil || resp.Error.Code != c.code {
			t.Errorf("params %v: error = %+v, want code %d", c.params, resp.Error, c.code)
		}
	}
}

// TestHostHandlers_MessageAndDisplayMode covers ui/message with the spec's
// single-object content and ui/request-display-mode's typed result.
func TestHostHandlers_MessageAndDisplayMode(t *testing.T) {
	var msg MessageRequest
	bridge := startHost(t, WithHostHandlers(HostHandlers{
		Message: func(_ context.Context, m MessageRequest) error {
			msg = m
			return nil
		},
		RequestDisplayMode: func(_ context.Context, r DisplayModeRequest) (DisplayModeResult, error) {
			return DisplayModeResult{Mode: "inline"}, nil // host declines fullscreen
		},
	}))

	resp, _ := bridge.SendToHost(context.Background(), MethodMessage, map[string]any{
		"role": "user", "content": map[string]any{"type": "text", "text": "summarise this"},
	})
	if resp.Error != nil {
		t.Fatalf("ui/message error: %+v", resp.Error)
	}
	if msg.Role != "user" || len(msg.Content) != 1 || msg.Content[0].Text != "summarise this" {
		t.Errorf("message = %+v", msg)
	}

	resp, _ = bridge.SendToHost(context.Background(), MethodRequestDisplayMode, map[string]any{"mode": "fullscreen"})
	raw, _ := ToBytes(resp.Result)
	var res DisplayModeResult
	json.Unmarshal(raw, &res)
	if res.Mode != "inline" {
		t.Errorf("display mode result = %s, want inline", raw)
	}
}

// TestHostHandlers_Notification verifies notifications other than
// tools/list_changed reach the host's Notification hook.
func TestHostHandlers_Notification(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	bridge := startHost(t, WithHostHandlers(HostHandlers{
		Notification: func(method string, _ json.RawMessage) {
			mu.Lock()
			methods = append(methods, method)
			mu.Unlock()
		},
	}))

	bridge.notifyHandler("ui/notifications/size-changed", json.RawMessage(`{"width":10,"height":20}`))
	bridge.notifyHandler("notifications/tools/list_changed", nil)

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != "ui/notifications/size-changed" {
		t.Errorf("notifications = %v", methods)
	}
}

// TestHostHandlers_BridgeWireFixture replays what the real bridge JS sends,
// recorded by mcp-app-bridge.test.ts into testdata/bridge-wire.json, through
// AppHost. The other tests here hand-build spec params, so they cannot catch
// the bridge and the host disagreeing on a shape. This one decodes the
// bridge's actual output and fails if any field lands empty.
func TestHostHandlers_BridgeWireFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/bridge-wire.json")
	if err != nil {
		t.Fatalf("read fixture (regenerate with UPDATE_FIXTURES=1 pnpm test in ext/ui/assets): %v", err)
	}
	var entries []struct {
		Name   string          `json:"name"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}

	var (
		mu            sync.Mutex
		modelCtx      ModelContextUpdate
		downloads     []DownloadFileRequest
		message       MessageRequest
		link          OpenLinkRequest
		mode          DisplayModeRequest
		notifications = map[string]json.RawMessage{}
	)
	bridge := startHost(t, WithHostHandlers(HostHandlers{
		UpdateModelContext: func(_ context.Context, u ModelContextUpdate) error { modelCtx = u; return nil },
		DownloadFile: func(_ context.Context, r DownloadFileRequest) error {
			downloads = append(downloads, r)
			return nil
		},
		Message:  func(_ context.Context, m MessageRequest) error { message = m; return nil },
		OpenLink: func(_ context.Context, r OpenLinkRequest) error { link = r; return nil },
		RequestDisplayMode: func(_ context.Context, r DisplayModeRequest) (DisplayModeResult, error) {
			mode = r
			return DisplayModeResult{Mode: r.Mode}, nil
		},
		Notification: func(method string, params json.RawMessage) {
			mu.Lock()
			notifications[method] = params
			mu.Unlock()
		},
	}))

	for _, e := range entries {
		if isHostMethod(e.Method) {
			resp, err := bridge.SendToHost(context.Background(), e.Method, e.Params)
			if err != nil {
				t.Fatalf("%s: %v", e.Name, err)
			}
			if resp.Error != nil {
				t.Errorf("%s: host answered %+v", e.Name, resp.Error)
			}
			continue
		}
		bridge.notifyHandler(e.Method, e.Params)
	}

	if len(modelCtx.Content) == 0 || modelCtx.Content[0].Text == "" || len(modelCtx.StructuredContent) == 0 {
		t.Errorf("updateModelContext decoded empty: %+v", modelCtx)
	}
	if len(downloads) != 2 {
		t.Fatalf("downloadFile calls = %d, want 2", len(downloads))
	}
	for i, d := range downloads {
		if len(d.Contents) == 0 {
			t.Errorf("downloadFile #%d decoded with no contents", i)
		}
		for _, c := range d.Contents {
			var item struct {
				Type     string          `json:"type"`
				URI      string          `json:"uri"`
				Resource json.RawMessage `json:"resource"`
			}
			if err := json.Unmarshal(c, &item); err != nil || item.Type == "" || (item.URI == "" && len(item.Resource) == 0) {
				t.Errorf("downloadFile #%d item has no type or target: %s", i, c)
			}
		}
	}
	if message.Role == "" || len(message.Content) == 0 || message.Content[0].Text == "" {
		t.Errorf("sendMessage decoded empty: %+v", message)
	}
	if link.URL == "" {
		t.Errorf("openLink decoded empty")
	}
	if mode.Mode == "" {
		t.Errorf("requestDisplayMode decoded empty")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := notifications["ui/notifications/request-teardown"]; !ok {
		got := make([]string, 0, len(notifications))
		for m := range notifications {
			got = append(got, m)
		}
		t.Errorf("requestTeardown did not reach Notification, got methods %v", got)
	}
	var logEntry struct {
		Level string          `json:"level"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(notifications["notifications/message"], &logEntry); err != nil || logEntry.Level == "" || len(logEntry.Data) == 0 {
		t.Errorf("log did not arrive as notifications/message with level and data: %s", notifications["notifications/message"])
	}
}
