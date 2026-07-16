package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
	tasksext "github.com/panyam/mcpkit/ext/tasks"
	"github.com/panyam/mcpkit/server"
)

// TestAppTaskWithInputPauseEndToEnd is issue 890's acceptance: a REAL
// ext/tasks (SEP-2663) server runs a task tool that parks in input_required
// via TaskElicit; agentchat polls, answers the pause through the scripted
// terminal elicitation UI, and delivers the final result — no bespoke code
// on the server side.
func TestAppTaskWithInputPauseEndToEnd(t *testing.T) {
	srv := server.NewServer(core.ServerInfo{Name: "tasks-e2e", Version: "0.0.1"})
	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("confirm_delete",
		"asks before deleting",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			tc := tasksext.GetTaskContext(ctx)
			if tc == nil {
				return core.GoAsyncResult{}, nil
			}
			result, err := tc.TaskElicit(core.ElicitationRequest{
				Message:         "Delete 'important.txt'?",
				RequestedSchema: json.RawMessage(`{"type":"object","properties":{"confirm":{"type":"boolean"}},"required":["confirm"]}`),
			})
			if err != nil {
				return core.TextResult(fmt.Sprintf("elicitation failed: %v", err)), nil
			}
			if confirmed, _ := result.Content["confirm"].(bool); result.Action == "accept" && confirmed {
				return core.TextResult("deleted 'important.txt'"), nil
			}
			return core.TextResult("kept 'important.txt'"), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))
	tasksext.Register(tasksext.Config{Server: srv})

	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "confirm_delete", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "The file has been deleted."},
	)

	var out strings.Builder
	stdin := strings.NewReader("y\n") // boolean confirm prompt
	app, err := NewApp(testConfig(ts.URL), &out, stdin, WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "delete important.txt"); err != nil {
		t.Fatal(err)
	}
	transcript := out.String()
	for _, want := range []string{
		"? Delete 'important.txt'?",
		"confirm (y/n):",
		"input_required",
		"✓ confirm_delete: deleted 'important.txt'",
		"The file has been deleted.",
	} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, transcript)
		}
	}
}
