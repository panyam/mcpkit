package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

func startServer(t *testing.T, srv *server.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	return ts
}

func connectClient(t *testing.T, url string, opts ...client.ClientOption) *client.Client {
	t.Helper()
	c := client.NewClient(url+"/mcp", core.ClientInfo{Name: "agent-elicit-test", Version: "1.0"}, opts...)
	if err := c.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func legacyElicitServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "legacy-elicit", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "ask_name", Description: "asks for a name", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			res, err := core.Elicit(ctx, core.ElicitationRequest{
				Message:         "What is your name?",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
			})
			if err != nil {
				return core.ErrorResult("elicit failed: " + err.Error()), nil
			}
			if res.Action != "accept" {
				return core.TextResult("user " + res.Action + "ed"), nil
			}
			name, _ := res.Content["name"].(string)
			return core.TextResult("Hello, " + name + "!"), nil
		},
	)
	return srv
}

func mrtrElicitServer(t *testing.T) *server.Server {
	t.Helper()
	srv := server.NewServer(core.ServerInfo{Name: "mrtr-elicit", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "ask_name", Description: "asks for a name", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if !ctx.HasInputResponses() {
				return ctx.RequestInput(core.InputRequests{
					"user_name": core.NewElicitationInputRequest(core.ElicitationRequest{
						Message:         "What is your name?",
						RequestedSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`),
					}),
				})
			}
			var er struct {
				Action  string `json:"action"`
				Content struct {
					Name string `json:"name"`
				} `json:"content"`
			}
			if err := json.Unmarshal(ctx.InputResponse("user_name"), &er); err != nil {
				return core.ErrorResult("malformed elicitation response"), nil
			}
			return core.TextResult("Hello, " + er.Content.Name + "!"), nil
		},
	)
	return srv
}

func acceptName(name string) ElicitationUI {
	return func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		return core.ElicitationResult{Action: "accept", Content: map[string]any{"name": name}}, nil
	}
}

func TestCoordinatorLegacyInlet(t *testing.T) {
	var calls atomic.Int32
	var seen core.ElicitationRequest
	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		calls.Add(1)
		seen = req
		return acceptName("Alice")(ctx, req)
	})

	ts := startServer(t, legacyElicitServer(t))
	c := connectClient(t, ts.URL, client.WithElicitationHandler(coord.Handler()))
	src := NewClientSource(c)

	res, err := src.Call(context.Background(), "ask_name", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "Hello, Alice!") {
		t.Fatalf("result = %+v", res)
	}
	if calls.Load() != 1 || seen.Message != "What is your name?" {
		t.Fatalf("UI invocations = %d, seen = %+v", calls.Load(), seen)
	}
}

func TestCoordinatorMRTRInlet(t *testing.T) {
	var calls atomic.Int32
	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		calls.Add(1)
		return acceptName("Bob")(ctx, req)
	})

	ts := startServer(t, mrtrElicitServer(t))
	c := connectClient(t, ts.URL, client.WithElicitationHandler(coord.Handler()))
	src := NewClientSource(c, WithInputHandler(client.DefaultInputHandler(c)))

	res, err := src.Call(context.Background(), "ask_name", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "Hello, Bob!") {
		t.Fatalf("result = %+v", res)
	}
	if calls.Load() != 1 {
		t.Fatalf("UI invocations = %d", calls.Load())
	}
}

func TestCoordinatorMRTRWithoutHandlerStillFailsFast(t *testing.T) {
	ts := startServer(t, mrtrElicitServer(t))
	c := connectClient(t, ts.URL)
	src := NewClientSource(c)

	_, err := src.Call(context.Background(), "ask_name", map[string]any{})
	if !errors.Is(err, ErrInputRequired) {
		t.Fatalf("want ErrInputRequired without a coordinator, got %v", err)
	}
}

func TestCoordinatorDeclinePropagates(t *testing.T) {
	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		return core.ElicitationResult{Action: "decline"}, nil
	})
	ts := startServer(t, legacyElicitServer(t))
	c := connectClient(t, ts.URL, client.WithElicitationHandler(coord.Handler()))
	src := NewClientSource(c)

	res, err := src.Call(context.Background(), "ask_name", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "user declineed") {
		t.Fatalf("decline must reach the tool as a result, got %+v", res)
	}
}

func TestCoordinatorFIFOSerialization(t *testing.T) {
	var active atomic.Int32
	var order []string
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})

	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		if active.Add(1) != 1 {
			t.Error("overlapping elicitations")
		}
		if req.Message == "first" {
			close(firstEntered)
			<-releaseFirst
		}
		order = append(order, req.Message)
		active.Add(-1)
		return core.ElicitationResult{Action: "accept"}, nil
	})

	done := make(chan string, 3)
	submit := func(msg string) {
		go func() {
			coord.present(context.Background(), core.ElicitationRequest{Message: msg})
			done <- msg
		}()
	}

	submit("first")
	<-firstEntered
	submit("second")
	time.Sleep(30 * time.Millisecond)
	submit("third")
	time.Sleep(30 * time.Millisecond)
	close(releaseFirst)

	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("serialization deadlock")
		}
	}
	if fmt.Sprint(order) != "[first second third]" {
		t.Fatalf("FIFO order violated: %v", order)
	}
}

func TestCoordinatorQueuedWaiterCancels(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		if req.Message == "blocker" {
			close(entered)
			<-release
		}
		return core.ElicitationResult{Action: "accept"}, nil
	})

	go coord.present(context.Background(), core.ElicitationRequest{Message: "blocker"})
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := coord.present(ctx, core.ElicitationRequest{Message: "queued"})
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued waiter must observe cancellation, got %v", err)
	}

	close(release)
	res, err := coord.present(context.Background(), core.ElicitationRequest{Message: "after"})
	if err != nil || res.Action != "accept" {
		t.Fatalf("queue must survive a cancelled waiter: %v %v", res, err)
	}
}

func TestCoordinatorURLModePassthrough(t *testing.T) {
	var seen core.ElicitationRequest
	coord := NewElicitationCoordinator(func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		seen = req
		return core.ElicitationResult{Action: "accept"}, nil
	})

	srv := server.NewServer(core.ServerInfo{Name: "url-elicit", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{Name: "ask_url", Description: "url-mode elicit", InputSchema: map[string]any{"type": "object"}},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			res, err := core.ElicitURL(ctx, core.ElicitationRequest{
				Message:       "Complete sign-in in your browser",
				Mode:          "url",
				URL:           "https://example.test/signin",
				ElicitationID: "e-1",
			})
			if err != nil {
				return core.ErrorResult("elicit failed: " + err.Error()), nil
			}
			return core.TextResult("action=" + res.Action), nil
		},
	)
	ts := startServer(t, srv)
	c := connectClient(t, ts.URL,
		client.WithElicitationHandler(coord.Handler()),
		client.WithElicitationURLSupport(),
	)
	src := NewClientSource(c)

	res, err := src.Call(context.Background(), "ask_url", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "action=accept") {
		t.Fatalf("result = %+v", res)
	}
	if seen.Mode != "url" || seen.URL != "https://example.test/signin" || seen.ElicitationID != "e-1" {
		t.Fatalf("url-mode request must reach the UI unmodified: %+v", seen)
	}
}
