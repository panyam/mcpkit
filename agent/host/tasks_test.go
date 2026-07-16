package host

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

// TestAppBackgroundTaskLifecycle is issue 914's acceptance: a long task
// detaches after the grace window, the user keeps conversing while it runs,
// and its completion surfaces as a transcript line plus injected context on
// the next turn.
func TestAppBackgroundTaskLifecycle(t *testing.T) {
	release := make(chan struct{})
	srv := server.NewServer(core.ServerInfo{Name: "bg-tasks-e2e", Version: "0.0.1"})
	srv.Register(core.TypedTool[struct{}, core.ToolResponse]("long_report",
		"builds a big report",
		func(ctx core.ToolContext, _ struct{}) (core.ToolResponse, error) {
			if tasksext.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			<-release
			return core.TextResult("report ready: 42 pages"), nil
		},
		core.WithToolExecution(&core.ToolExecution{TaskSupport: core.TaskSupportRequired}),
	))
	tasksext.Register(tasksext.Config{Server: srv})
	ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true)))
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	stub := agent.NewStubProvider(
		agent.StubTurn{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "long_report", Args: core.NewRawJSON(json.RawMessage(`{}`))}}},
		agent.StubTurn{Text: "Started your report; I'll let you know."},
		agent.StubTurn{Text: "Doing fine, report still cooking."},
		agent.StubTurn{Text: "Your report is done: 42 pages."},
	)
	out := &syncWriter{}
	cfg := testConfig(ts.URL)
	cfg.TaskGraceSec = 1
	app, err := NewApp(cfg, out, strings.NewReader(""), WithProvider(stub))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	if err := app.RunTurn(context.Background(), "build me the big report"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "moved to background") {
		t.Fatalf("detach line missing:\n%s", out.String())
	}
	if got := len(app.snapshotTasks()); got != 1 {
		t.Fatalf("registry must hold the running task, got %d", got)
	}

	// The conversation continues while the task runs.
	if err := app.RunTurn(context.Background(), "how are you?"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "report still cooking") {
		t.Fatalf("second turn must run while task is in background:\n%s", out.String())
	}

	close(release)
	waitFor(t, out, "completed: report ready: 42 pages")
	if got := len(app.snapshotTasks()); got != 0 {
		t.Fatalf("registry must drop completed tasks, got %d", got)
	}

	if err := app.RunTurn(context.Background(), "any news?"); err != nil {
		t.Fatal(err)
	}
	final := stub.Requests()[3]
	var injected bool
	for _, m := range final.Messages {
		if m.Role == agent.RoleSystem && strings.Contains(m.Text, "task.completed") && strings.Contains(m.Text, "report ready") {
			injected = true
		}
	}
	if !injected {
		t.Fatalf("completion must inject as context on the next turn: %+v", final.Messages)
	}
}
