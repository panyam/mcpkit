package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
	gohttp "github.com/panyam/servicekit/http"
)

// 2026-07-28 streams are not resumable: a server ignores Last-Event-ID, and
// so emits no SSE event ids to resume from (#825). These pin that on both
// stateless stream shapes, with an event store configured, since that is
// what turns resumption on for the session wire.

func statelessResumptionServer(t *testing.T) string {
	t.Helper()
	srv := NewServer(core.ServerInfo{Name: "resumption", Version: "1"})
	srv.HandleMethod("test/streamemit", func(ctx core.MethodContext, id json.RawMessage, _ json.RawMessage) *core.Response {
		ctx.Notify("test/notify", map[string]any{"seq": 1})
		return core.NewResponse(id, map[string]any{"done": true})
	})
	ts := httptest.NewServer(srv.Handler(WithStreamableHTTP(true), WithStatelessMode(stateless.ModeStateless),
		WithEventStore(gohttp.NewMemoryEventStore(100))))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func sseIDLines(body string) []string {
	var ids []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id:") {
			ids = append(ids, line)
		}
	}
	return ids
}

func TestStatelessPostSSE_NoEventIDsAndLastEventIDIgnored(t *testing.T) {
	url := statelessResumptionServer(t)
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "test/streamemit", "params": validMetaParams()})
	req, _ := http.NewRequestWithContext(context.Background(), "POST", url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set(mcpProtocolVersionHeader, draftVersion)
	req.Header.Set("Last-Event-ID", "1")
	stampRoutingHeaders(t, req, raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"done":true`) {
		t.Fatalf("status %d body %s, want the call served despite Last-Event-ID", resp.StatusCode, body)
	}
	if got := strings.Count(string(body), "data:"); got != 2 {
		t.Errorf("%d data frames, want the notification and the result and nothing replayed: %s", got, body)
	}
	if ids := sseIDLines(string(body)); len(ids) != 0 {
		t.Errorf("stateless POST SSE carries event ids %v, want none", ids)
	}
}

func TestStatelessListen_NoEventIDsAndLastEventIDIgnored(t *testing.T) {
	url := statelessResumptionServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "subscriptions/listen",
		"params": map[string]any{"_meta": validMetaParams()["_meta"], "notifications": map[string]bool{"toolsListChanged": true}},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(mcpProtocolVersionHeader, draftVersion)
	req.Header.Set("Last-Event-ID", "1")
	stampRoutingHeaders(t, req, raw)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	// Read up to the acknowledgement, the stream's first frame.
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if strings.Contains(sc.Text(), "notifications/subscriptions/acknowledged") {
			break
		}
	}
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "notifications/subscriptions/acknowledged") {
		t.Fatalf("no acknowledgement on the listen stream (status %d): %s", resp.StatusCode, got)
	}
	if ids := sseIDLines(got); len(ids) != 0 {
		t.Errorf("subscriptions/listen stream carries event ids %v, want none", ids)
	}
}
