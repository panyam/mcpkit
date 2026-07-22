package host

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

type fakeSource struct {
	result *core.ToolResult
	err    error
}

func (f *fakeSource) Tools(context.Context) ([]core.ToolDef, error) { return nil, nil }
func (f *fakeSource) Call(context.Context, string, map[string]any) (*core.ToolResult, error) {
	return f.result, f.err
}

func TestAvailabilitySource(t *testing.T) {
	// A transient (network) error means the server is unreachable -> map to a
	// non-fatal ErrNotAvailableNow naming the server.
	s := newAvailabilitySource(&fakeSource{err: io.EOF}, "atlassian")
	_, err := s.Call(context.Background(), "create_issue", nil)
	if !errors.Is(err, agent.ErrNotAvailableNow) {
		t.Fatalf("transient error should map to ErrNotAvailableNow, got %v", err)
	}
	if !strings.Contains(err.Error(), "atlassian") || !strings.Contains(err.Error(), "create_issue") {
		t.Fatalf("message should name the server and tool: %v", err)
	}

	// A non-transient error (server said no) passes through unchanged.
	s = newAvailabilitySource(&fakeSource{err: errors.New("bad request")}, "atlassian")
	if _, err := s.Call(context.Background(), "x", nil); errors.Is(err, agent.ErrNotAvailableNow) {
		t.Fatalf("non-transient error must NOT map to ErrNotAvailableNow: %v", err)
	}

	// A successful call (including a tool-side IsError result) passes through.
	want := &core.ToolResult{IsError: true}
	s = newAvailabilitySource(&fakeSource{result: want}, "atlassian")
	got, err := s.Call(context.Background(), "x", nil)
	if err != nil || got != want {
		t.Fatalf("success/IsError result must pass through: got=%v err=%v", got, err)
	}
}
