package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func offloadConfig(url string) *Config {
	c := testConfig(url)
	c.Offload = &OffloadConfig{ThresholdBytes: 1024, PreviewLen: 30}
	return c
}

func toolMsg(t *testing.T, msgs []agent.Message) agent.Message {
	t.Helper()
	for _, m := range msgs {
		if m.Role == agent.RoleTool {
			return m
		}
	}
	t.Fatalf("no RoleTool message in %+v", msgs)
	return agent.Message{}
}

func TestAppOffloadsLargeToolResult(t *testing.T) {
	ts := startTestServer(t)
	big := strings.Repeat("x", 4000)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"` + big + `"}`)),
		}}},
		agent.StubTurn{Text: "done"},
	)
	store := agent.NewInMemoryToolResultStore()
	var out strings.Builder
	app, err := NewApp(offloadConfig(ts.URL), &out, strings.NewReader(""),
		WithProvider(stub), WithToolResultStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	// the result fed back to the model on the second call is the stub
	fed := toolMsg(t, stub.Requests()[1].Messages).Text
	if strings.Contains(fed, big) {
		t.Fatal("full payload leaked into the conversation; offloading did not fire")
	}
	if !strings.Contains(fed, "stored as res:") || !strings.Contains(fed, agent.ReadToolResultName) {
		t.Fatalf("tool message is not an offload stub: %q", fed)
	}

	// read_tool_result is offered to the model
	var offered bool
	for _, d := range stub.Requests()[0].Tools {
		if d.Name == agent.ReadToolResultName {
			offered = true
		}
	}
	if !offered {
		t.Fatal("read_tool_result was not offered to the model")
	}

	// the blob is retrievable and complete
	ref := fed[strings.Index(fed, "res:"):]
	ref = ref[:strings.IndexFunc(ref, func(r rune) bool { return r == ']' || r == '\n' || r == ' ' })]
	resp, err := store.GetToolResult(context.Background(), agent.GetToolResultRequest{Ref: ref})
	if err != nil || !resp.Found {
		t.Fatalf("offloaded blob %q not in store: %v", ref, err)
	}
	if !strings.Contains(resp.Result.Content[0].Text, big) {
		t.Fatal("stored blob does not contain the full payload")
	}
}

func TestAppNoOffloadWhenDisabled(t *testing.T) {
	ts := startTestServer(t)
	big := strings.Repeat("y", 4000)
	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{
			ID: "c1", Name: "echo", Args: core.NewRawJSON(json.RawMessage(`{"message":"` + big + `"}`)),
		}}},
		agent.StubTurn{Text: "done"},
	)
	var out strings.Builder
	// testConfig has no Offload -> results flow verbatim
	app, err := NewApp(testConfig(ts.URL), &out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.RunTurn(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolMsg(t, stub.Requests()[1].Messages).Text, big) {
		t.Fatal("result was altered though offloading is off")
	}
}
