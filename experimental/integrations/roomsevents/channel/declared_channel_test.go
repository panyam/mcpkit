package channel

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
	rt "github.com/panyam/mcpkit/experimental/integrations/roomsevents/internal/roomstest"
)

// A type a page declared reaches Claude Code like the built-in ones: attach
// subscribes to every room event type the bridge lists, not just three.
func TestFrontEnd_DeliversAPageDeclaredEventType(t *testing.T) {
	st := rt.New(t, rooms.WithPageEventCatalog([]rooms.PageEventDecl{{
		Name:          "item.found",
		PayloadSchema: map[string]any{"type": "object", "properties": map[string]any{"item": map[string]any{"type": "string"}}},
	}}))
	f, err := New(t.Context(), Config{BridgeURL: st.URL, Token: rt.Token("alice"), Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(f.Close)
	h := startHost(t, f)
	_ = h.request(1, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "claude-code", "version": "2"},
	})
	_, _ = h.w.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	_ = h.request(2, "tools/call", map[string]any{"name": "attach", "arguments": map[string]any{"resource": rt.Room}})

	p := st.Page(t, "alice", rt.Room)
	require.Eventually(t, func() bool { return f.bridgeSeesPage(p.ID()) }, 3*time.Second, 10*time.Millisecond)
	p.SetAgentsAllowed(true)
	st.WaitAgreed(t, f.bridge.SessionID(), rt.Room, p.ID())
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, p.Emit("item.found", map[string]any{"item": "brass key"}))
	require.Eventually(t, func() bool { return len(h.channelMessages()) == 1 }, 3*time.Second, 10*time.Millisecond)
	var content string
	var meta map[string]string
	msg := h.channelMessages()[0]
	require.NoError(t, json.Unmarshal(msg["content"], &content))
	require.NoError(t, json.Unmarshal(msg["meta"], &meta))
	assert.Equal(t, "item.found", meta["event"])
	assert.Contains(t, content, "sent item.found")
	assert.Contains(t, content, "brass key")
}
