package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/panyam/mcpkit/core"
)

// countingAsk returns an AskFunc that always answers `answer` and a pointer to
// the number of times it was consulted, so a test can assert both the verdict
// and whether the ask seam was reached at all.
func countingAsk(answer bool) (AskFunc, *int) {
	n := 0
	return func(ctx context.Context, info ToolCallInfo) (bool, error) {
		n++
		return answer, nil
	}, &n
}

// gate runs the policy as middleware and reports the outcome in the shape the
// assertions below care about: whether the call was allowed through to next,
// and the reason when it was refused. A denial is an error carrying
// ToolDeniedError rather than a returned struct, so this unwraps it once here
// instead of at every call site.
func gate(p *TieredApproval, info ToolCallInfo) (allowed bool, reason string, err error) {
	reached := false
	next := func(context.Context, ToolCallInfo) (*core.ToolResult, error) {
		reached = true
		return &core.ToolResult{}, nil
	}
	if _, e := p.WrapToolCall(context.Background(), info, next); e != nil {
		if r, ok := deniedReason(e); ok {
			return false, r, nil
		}
		return false, "", e
	}
	return reached, "", nil
}

func req(tool string, readOnly bool) ToolCallInfo {
	return ToolCallInfo{Call: ToolCall{Name: tool}, ReadOnly: readOnly}
}

func TestTieredApprovalModes(t *testing.T) {

	t.Run("always-allow skips the ask", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithAsk(ask))
		allowed, _, err := gate(p, req("write_file", false))
		if err != nil || !allowed {
			t.Fatalf("allowed=%v err=%v", allowed, err)
		}
		if *n != 0 {
			t.Fatalf("ask consulted %d times, want 0", *n)
		}
	})

	t.Run("read-only-auto allows read-only without asking", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeReadOnlyAuto), WithAsk(ask))
		allowed, _, _ := gate(p, req("list_files", true))
		if !allowed || *n != 0 {
			t.Fatalf("read-only should auto-allow: allowed=%v asks=%d", allowed, *n)
		}
	})

	t.Run("read-only-auto asks for mutating tools", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeReadOnlyAuto), WithAsk(ask))
		allowed, _, _ := gate(p, req("delete_file", false))
		if !allowed || *n != 1 {
			t.Fatalf("mutating tool should ask: allowed=%v asks=%d", allowed, *n)
		}
	})

	t.Run("always-ask asks even for read-only", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithAsk(ask))
		if _, _, _ = gate(p, req("list_files", true)); *n != 1 {
			t.Fatalf("always-ask should consult ask for read-only too: asks=%d", *n)
		}
	})
}

func TestTieredApprovalRulesOverrideMode(t *testing.T) {

	t.Run("deny rule refuses without asking", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithToolRule("deploy", RuleDeny), WithAsk(ask))
		allowed, reason, _ := gate(p, req("deploy", false))
		if allowed || reason == "" || *n != 0 {
			t.Fatalf("deny rule should refuse silently: allowed=%v reason=%q asks=%d", allowed, reason, *n)
		}
	})

	t.Run("allow rule wins over always-ask", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithToolRule("read_status", RuleAllow), WithAsk(ask))
		allowed, _, _ := gate(p, req("read_status", false))
		if !allowed || *n != 0 {
			t.Fatalf("allow rule should auto-allow: allowed=%v asks=%d", allowed, *n)
		}
	})

	t.Run("ask rule wins over always-allow", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithToolRule("send_email", RuleAsk), WithAsk(ask))
		if _, _, _ = gate(p, req("send_email", false)); *n != 1 {
			t.Fatalf("ask rule should force a prompt even in yolo mode: asks=%d", *n)
		}
	})
}

func TestTieredApprovalRemembersApproval(t *testing.T) {
	ask, n := countingAsk(true)
	p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithAsk(ask), WithRememberApprovals(true))

	if allowed, _, _ := gate(p, req("send_email", false)); !allowed {
		t.Fatal("first call should be allowed after the user approves")
	}
	if allowed, _, _ := gate(p, req("send_email", false)); !allowed {
		t.Fatal("second call should be auto-allowed from the remember cache")
	}
	if *n != 1 {
		t.Fatalf("ask consulted %d times, want 1 (second call cached)", *n)
	}
}

func TestTieredApprovalDeclineAndNoUI(t *testing.T) {

	t.Run("decline refuses with a reason and is not remembered", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithAsk(ask), WithRememberApprovals(true))
		allowed, reason, _ := gate(p, req("send_email", false))
		if allowed || reason == "" {
			t.Fatalf("decline should refuse with reason: allowed=%v reason=%q", allowed, reason)
		}
		if _, _, _ = gate(p, req("send_email", false)); *n != 2 {
			t.Fatalf("a declined tool must not be cached: asks=%d, want 2", *n)
		}
	})

	t.Run("no ask wired fails closed", func(t *testing.T) {
		p := NewTieredApproval() // ModeAlwaysAsk, no AskFunc
		allowed, reason, err := gate(p, req("anything", false))
		if err != nil || allowed || reason == "" {
			t.Fatalf("missing ask UI should refuse, not error: allowed=%v reason=%q err=%v", allowed, reason, err)
		}
	})

	t.Run("ask error propagates", func(t *testing.T) {
		boom := errors.New("ui gone")
		p := NewTieredApproval(WithAsk(func(context.Context, ToolCallInfo) (bool, error) { return false, boom }))
		if _, _, err := gate(p, req("x", false)); !errors.Is(err, boom) {
			t.Fatalf("ask error should propagate, got %v", err)
		}
	})
}

func TestToolReadOnly(t *testing.T) {
	tools := []core.ToolDef{
		{Name: "list", Annotations: map[string]any{"readOnlyHint": true}},
		{Name: "write", Annotations: map[string]any{"readOnlyHint": false}},
		{Name: "plain"},
	}
	cases := map[string]bool{"list": true, "write": false, "plain": false, "unknown": false}
	for name, want := range cases {
		if got := toolReadOnly(tools, name); got != want {
			t.Errorf("toolReadOnly(%q) = %v, want %v", name, got, want)
		}
	}
}
