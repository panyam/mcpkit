package rooms_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawSession speaks the session wire by hand, for the two things the Go
// client does not expose: choosing a request id to cancel, and reading the
// progress notifications that precede a result.
type rawSession struct {
	t   *testing.T
	url string
	sid string
}

func openRaw(t *testing.T, url string) *rawSession {
	t.Helper()
	r := &rawSession{t: t, url: url}
	r.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
	r.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return r
}

// post sends one message and returns every JSON-RPC message in the response,
// whether it came back as JSON or as an SSE stream.
func (r *rawSession) post(body string) []map[string]any {
	r.t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.url, strings.NewReader(body))
	require.NoError(r.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if r.sid != "" {
		req.Header.Set("Mcp-Session-Id", r.sid)
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		r.sid = sid
	}
	var out []map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var m map[string]any
				if json.Unmarshal([]byte(data), &m) == nil {
					out = append(out, m)
				}
			}
		}
		return out
	}
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(b), &m) == nil {
		out = append(out, m)
	}
	return out
}

func (r *rawSession) call(id int, name string, args map[string]any, meta map[string]any) []map[string]any {
	params := map[string]any{"name": name, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params})
	return r.post(string(b))
}

// waitPaired polls list_pages until page is this session's default target.
func (r *rawSession) waitPaired(page rooms.PeerID) {
	r.t.Helper()
	id := 100
	require.Eventually(r.t, func() bool {
		id++
		for _, m := range r.call(id, "list_pages", map[string]any{}, nil) {
			var out struct {
				Result struct {
					StructuredContent struct {
						Pages []rooms.PageInfo `json:"pages"`
					} `json:"structuredContent"`
				} `json:"result"`
			}
			_ = json.Unmarshal([]byte(mustString(m)), &out)
			for _, p := range out.Result.StructuredContent.Pages {
				if p.Page == page && p.Paired {
					return true
				}
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
}

// Acceptance 4: an agent's notifications/cancelled reaches the page's execute
// as a cancelled context.
func TestBridge_AgentCancelReachesExecute(t *testing.T) {
	st := newLocalStack(t, rooms.WithCallTimeout(time.Minute))
	started, aborted := make(chan struct{}), make(chan struct{})
	p := st.page(t, "alice", room, rooms.PageTool{
		Name: "slow",
		Execute: func(ctx context.Context, _ rooms.PageCall) (core.ToolResult, error) {
			close(started)
			<-ctx.Done()
			close(aborted)
			return core.ToolResult{}, ctx.Err()
		},
	})
	p.SetAgentsAllowed(true)
	r := openRaw(t, st.url)
	r.call(2, "attach", map[string]any{"resource": room}, nil)
	r.waitPaired(p.ID())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r.call(7, "slow", map[string]any{}, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the call never reached the page")
	}
	r.post(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7,"reason":"user changed their mind"}}`)
	select {
	case <-aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("the agent's cancel never reached execute")
	}
	wg.Wait()
}

// The page reports progress against the bridge's call id; the agent receives
// it under its own progress token.
func TestBridge_ProgressRelayedUnderAgentToken(t *testing.T) {
	st := newLocalStack(t)
	p := st.page(t, "alice", room, rooms.PageTool{
		Name: "render",
		Execute: func(_ context.Context, call rooms.PageCall) (core.ToolResult, error) {
			call.Progress(1, 2, "half")
			call.Progress(2, 2, "done")
			return core.TextResult("rendered"), nil
		},
	})
	p.SetAgentsAllowed(true)
	r := openRaw(t, st.url)
	r.call(2, "attach", map[string]any{"resource": room}, nil)
	r.waitPaired(p.ID())

	msgs := r.call(3, "render", map[string]any{}, map[string]any{"progressToken": "agent-token-9"})
	var progress []string
	var result map[string]any
	for _, m := range msgs {
		if m["method"] == "notifications/progress" {
			params := m["params"].(map[string]any)
			assert.Equal(t, "agent-token-9", params["progressToken"])
			progress = append(progress, params["message"].(string))
		}
		if m["id"] == float64(3) {
			result, _ = m["result"].(map[string]any)
		}
	}
	assert.Equal(t, []string{"half", "done"}, progress)
	require.NotNil(t, result, "%v", msgs)
	assert.Contains(t, mustString(result), "rendered")
}

func mustString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A reply carrying a live call id from a peer the call did not go to is
// dropped, and so is a reply to an id the bridge never issued.
func TestBridge_DropsRepliesFromOtherPeersAndUnknownIDs(t *testing.T) {
	st := newStack(t)
	release := make(chan struct{})
	p := st.page(t, "alice", room, rooms.PageTool{
		Name: "show",
		Execute: func(context.Context, rooms.PageCall) (core.ToolResult, error) {
			<-release
			return core.TextResult("genuine"), nil
		},
	})
	p.SetAgentsAllowed(true)
	a := st.agent(t, "alice", allScopes...)
	a.attach(t, room)
	a.waitPage(t, p.ID(), func(pi rooms.PageInfo) bool { return pi.Paired })

	rogue, err := st.relay.Transport("mallory").Join(t.Context(), room)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rogue.Leave() })

	done := make(chan string, 1)
	go func() {
		res, err := a.ToolCall(t.Context(), "show", map[string]any{})
		require.NoError(t, err)
		done <- res
	}()

	var callID json.RawMessage
	var bridgeID rooms.PeerID
	require.Eventually(t, func() bool {
		for _, d := range st.relay.Deliveries() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(d.Payload, &m)
			if d.To == p.ID() && m.Method == "tools/call" {
				callID, bridgeID = m.ID, d.From
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)

	forged := func(id json.RawMessage) json.RawMessage {
		return json.RawMessage(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"content":[{"type":"text","text":"forged"}]}}`)
	}
	require.NoError(t, rogue.Send(t.Context(), bridgeID, rooms.MessageKind, forged(callID)))
	require.NoError(t, rogue.Send(t.Context(), bridgeID, rooms.MessageKind, forged(json.RawMessage(`"no-such-call"`))))
	select {
	case got := <-done:
		t.Fatalf("the call completed with a reply the target never sent: %q", got)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case got := <-done:
		assert.Equal(t, "genuine", got)
	case <-time.After(3 * time.Second):
		t.Fatal("the genuine reply never arrived")
	}
}
