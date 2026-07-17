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
	return func(ctx context.Context, req ApprovalRequest) (bool, error) {
		n++
		return answer, nil
	}, &n
}

func req(tool string, readOnly bool) ApprovalRequest {
	return ApprovalRequest{ToolName: tool, ReadOnly: readOnly}
}

func TestTieredApprovalModes(t *testing.T) {
	ctx := context.Background()

	t.Run("always-allow skips the ask", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithAsk(ask))
		dec, err := p.Approve(ctx, req("write_file", false))
		if err != nil || !dec.Allowed {
			t.Fatalf("dec=%+v err=%v", dec, err)
		}
		if *n != 0 {
			t.Fatalf("ask consulted %d times, want 0", *n)
		}
	})

	t.Run("read-only-auto allows read-only without asking", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeReadOnlyAuto), WithAsk(ask))
		dec, _ := p.Approve(ctx, req("list_files", true))
		if !dec.Allowed || *n != 0 {
			t.Fatalf("read-only should auto-allow: dec=%+v asks=%d", dec, *n)
		}
	})

	t.Run("read-only-auto asks for mutating tools", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeReadOnlyAuto), WithAsk(ask))
		dec, _ := p.Approve(ctx, req("delete_file", false))
		if !dec.Allowed || *n != 1 {
			t.Fatalf("mutating tool should ask: dec=%+v asks=%d", dec, *n)
		}
	})

	t.Run("always-ask asks even for read-only", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithAsk(ask))
		if _, _ = p.Approve(ctx, req("list_files", true)); *n != 1 {
			t.Fatalf("always-ask should consult ask for read-only too: asks=%d", *n)
		}
	})
}

func TestTieredApprovalRulesOverrideMode(t *testing.T) {
	ctx := context.Background()

	t.Run("deny rule refuses without asking", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithToolRule("deploy", RuleDeny), WithAsk(ask))
		dec, _ := p.Approve(ctx, req("deploy", false))
		if dec.Allowed || dec.Reason == "" || *n != 0 {
			t.Fatalf("deny rule should refuse silently: dec=%+v asks=%d", dec, *n)
		}
	})

	t.Run("allow rule wins over always-ask", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithToolRule("read_status", RuleAllow), WithAsk(ask))
		dec, _ := p.Approve(ctx, req("read_status", false))
		if !dec.Allowed || *n != 0 {
			t.Fatalf("allow rule should auto-allow: dec=%+v asks=%d", dec, *n)
		}
	})

	t.Run("ask rule wins over always-allow", func(t *testing.T) {
		ask, n := countingAsk(true)
		p := NewTieredApproval(WithDefaultMode(ModeAlwaysAllow), WithToolRule("send_email", RuleAsk), WithAsk(ask))
		if _, _ = p.Approve(ctx, req("send_email", false)); *n != 1 {
			t.Fatalf("ask rule should force a prompt even in yolo mode: asks=%d", *n)
		}
	})
}

func TestTieredApprovalRemembersApproval(t *testing.T) {
	ctx := context.Background()
	ask, n := countingAsk(true)
	p := NewTieredApproval(WithDefaultMode(ModeAlwaysAsk), WithAsk(ask), WithRememberApprovals(true))

	if dec, _ := p.Approve(ctx, req("send_email", false)); !dec.Allowed {
		t.Fatal("first call should be allowed after the user approves")
	}
	if dec, _ := p.Approve(ctx, req("send_email", false)); !dec.Allowed {
		t.Fatal("second call should be auto-allowed from the remember cache")
	}
	if *n != 1 {
		t.Fatalf("ask consulted %d times, want 1 (second call cached)", *n)
	}
}

func TestTieredApprovalDeclineAndNoUI(t *testing.T) {
	ctx := context.Background()

	t.Run("decline refuses with a reason and is not remembered", func(t *testing.T) {
		ask, n := countingAsk(false)
		p := NewTieredApproval(WithAsk(ask), WithRememberApprovals(true))
		dec, _ := p.Approve(ctx, req("send_email", false))
		if dec.Allowed || dec.Reason == "" {
			t.Fatalf("decline should refuse with reason: %+v", dec)
		}
		if _, _ = p.Approve(ctx, req("send_email", false)); *n != 2 {
			t.Fatalf("a declined tool must not be cached: asks=%d, want 2", *n)
		}
	})

	t.Run("no ask wired fails closed", func(t *testing.T) {
		p := NewTieredApproval() // ModeAlwaysAsk, no AskFunc
		dec, err := p.Approve(ctx, req("anything", false))
		if err != nil || dec.Allowed || dec.Reason == "" {
			t.Fatalf("missing ask UI should refuse, not error: dec=%+v err=%v", dec, err)
		}
	})

	t.Run("ask error propagates", func(t *testing.T) {
		boom := errors.New("ui gone")
		p := NewTieredApproval(WithAsk(func(context.Context, ApprovalRequest) (bool, error) { return false, boom }))
		if _, err := p.Approve(ctx, req("x", false)); !errors.Is(err, boom) {
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
