package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
	"github.com/panyam/mcpkit/core"
)

// stubProposer returns fixed proposals, so the approval path is testable
// without a model.
type stubProposer struct {
	out    []Proposal
	err    error
	sawGap []Gap
}

func (p *stubProposer) Propose(_ context.Context, gaps []Gap) ([]Proposal, error) {
	p.sawGap = gaps
	return p.out, p.err
}

// recordingSource records dispatches so a test can prove a proposal did or did
// not run.
type recordingSource struct {
	calls []string
	err   error
}

func (s *recordingSource) Tools(context.Context) ([]core.ToolDef, error) { return nil, nil }

func (s *recordingSource) Call(_ context.Context, name string, _ map[string]any) (*core.ToolResult, error) {
	s.calls = append(s.calls, name)
	return &core.ToolResult{}, s.err
}

func extWithProposals(t *testing.T, cfg Config) (*Extension, string) {
	t.Helper()
	work := t.TempDir()
	cfg.Root = filepath.Join(t.TempDir(), "cp")
	cfg.Writes = []WriteSpec{{Tool: "write_file", Paths: pathArg}}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e, work
}

// runTurnWithGap produces one restored file and one unreversible call, which
// is the state /undo has to report on.
func runTurnWithGap(t *testing.T, e *Extension, work string) {
	t.Helper()
	f := filepath.Join(work, "a.txt")
	write(t, f, "original")
	if err := e.TurnStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("write_file", map[string]any{"path": f})},
		func() { write(t, f, "edited") }); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(t, e, agent.ToolCallInfo{Call: call("create_issue", map[string]any{"title": "bug"})}, nil); err != nil {
		t.Fatal(err)
	}
}

// TestProposalNeverRunsWithoutApproval is the invariant the whole tier rests
// on. A proposal is a guess at an operation nobody could reverse
// automatically, aimed at a tool that changes things. No approval prompt means
// nothing runs, rather than everything.
func TestProposalNeverRunsWithoutApproval(t *testing.T) {
	src := &recordingSource{}
	prop := &stubProposer{out: []Proposal{{Tool: "delete_issue", Args: map[string]any{"id": 41}}}}
	e, work := extWithProposals(t, Config{Proposer: prop, Tools: src}) // Approve deliberately nil
	runTurnWithGap(t, e, work)

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("a proposal ran with no approval wired: %v", src.calls)
	}
	if !strings.Contains(res.Message, "NOT RUN") {
		t.Fatalf("report did not say the proposal was skipped: %q", res.Message)
	}
}

func TestDeclinedProposalDoesNotRun(t *testing.T) {
	src := &recordingSource{}
	prop := &stubProposer{out: []Proposal{{Tool: "delete_issue"}}}
	e, work := extWithProposals(t, Config{
		Proposer: prop,
		Tools:    src,
		Approve:  func(context.Context, Proposal) (bool, error) { return false, nil },
	})
	runTurnWithGap(t, e, work)

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("a declined proposal ran: %v", src.calls)
	}
	if !strings.Contains(res.Message, "declined") {
		t.Fatalf("report = %q", res.Message)
	}
}

func TestApprovedProposalRuns(t *testing.T) {
	src := &recordingSource{}
	prop := &stubProposer{out: []Proposal{{Tool: "delete_issue", Args: map[string]any{"id": 41}}}}
	var asked []string
	e, work := extWithProposals(t, Config{
		Proposer: prop,
		Tools:    src,
		Approve: func(_ context.Context, p Proposal) (bool, error) {
			asked = append(asked, p.Tool)
			return true, nil
		},
	})
	runTurnWithGap(t, e, work)

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "delete_issue" {
		t.Fatalf("approval was asked %v, want one delete_issue", asked)
	}
	if len(src.calls) != 1 || src.calls[0] != "delete_issue" {
		t.Fatalf("dispatched %v, want [delete_issue]", src.calls)
	}
	if !strings.Contains(res.Message, "ran") {
		t.Fatalf("report = %q", res.Message)
	}
}

// TestApprovalErrorDoesNotRun pins the fail-closed direction: an approval
// prompt that breaks is a reason not to act.
func TestApprovalErrorDoesNotRun(t *testing.T) {
	src := &recordingSource{}
	prop := &stubProposer{out: []Proposal{{Tool: "delete_issue"}}}
	e, work := extWithProposals(t, Config{
		Proposer: prop,
		Tools:    src,
		Approve:  func(context.Context, Proposal) (bool, error) { return false, errors.New("ui gone") },
	})
	runTurnWithGap(t, e, work)

	if _, err := e.runUndo(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 0 {
		t.Fatalf("a proposal ran despite an approval error: %v", src.calls)
	}
}

// TestProposerOnlySeesGaps pins that the proposer is handed the gap list and
// nothing else — not the restored files, not the turn.
func TestProposerOnlySeesGaps(t *testing.T) {
	prop := &stubProposer{}
	e, work := extWithProposals(t, Config{Proposer: prop})
	runTurnWithGap(t, e, work)

	if _, err := e.runUndo(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if len(prop.sawGap) != 1 || prop.sawGap[0].Tool != "create_issue" {
		t.Fatalf("proposer saw %+v, want exactly the create_issue gap", prop.sawGap)
	}
}

func TestNoProposerJustReports(t *testing.T) {
	e, work := extWithProposals(t, Config{})
	runTurnWithGap(t, e, work)
	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Message, "create_issue") {
		t.Fatalf("gap report lost: %q", res.Message)
	}
	if strings.Contains(res.Message, "proposed") {
		t.Fatalf("no proposer should mean no proposal section: %q", res.Message)
	}
}

func TestProposerErrorIsReportedNotFatal(t *testing.T) {
	prop := &stubProposer{err: errors.New("model unreachable")}
	e, work := extWithProposals(t, Config{Proposer: prop})
	runTurnWithGap(t, e, work)

	res, err := e.runUndo(context.Background(), "")
	if err != nil {
		t.Fatalf("a proposer failure must not fail the restore: %v", err)
	}
	if !strings.Contains(res.Message, "file(s) restored") {
		t.Fatalf("restore report lost: %q", res.Message)
	}
	if !strings.Contains(res.Message, "model unreachable") {
		t.Fatalf("proposer error not surfaced: %q", res.Message)
	}
}

// TestModelProposerSeedsAFreshConversation is the security pin. If the turn
// went wrong through prompt injection, running the cleanup inside that context
// asks the attacker to write the cleanup. The proposer's request must carry
// only the gap list.
func TestModelProposerSeedsAFreshConversation(t *testing.T) {
	answer, _ := json.Marshal(map[string]any{
		"proposals": []map[string]any{{
			"gapTool": "create_issue", "tool": "delete_issue",
			"args": map[string]any{"id": 41}, "rationale": "removes the issue that was filed",
		}},
	})
	stub := agent.NewStubProvider(
		agent.StubTurn{Text: "here are the offsets"}, // terminal text
		agent.StubTurn{Text: string(answer)},         // finalizing coercion
	)
	r, err := agent.NewRunner(agent.RunnerConfig{Provider: stub, ResponseSchema: ProposalSchema()})
	if err != nil {
		t.Fatal(err)
	}
	mp, err := NewModelProposer(r)
	if err != nil {
		t.Fatal(err)
	}

	got, err := mp.Propose(context.Background(), []Gap{{Tool: "create_issue", Args: `{"title":"bug"}`}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Tool != "delete_issue" {
		t.Fatalf("proposals = %+v", got)
	}
	if got[0].Gap.Tool != "create_issue" {
		t.Fatalf("proposal not linked back to its gap: %+v", got[0])
	}

	reqs := stub.Requests()
	if len(reqs) == 0 {
		t.Fatal("proposer made no model call")
	}
	msgs := reqs[0].Messages
	if len(msgs) != 1 {
		t.Fatalf("proposer was seeded with %d messages, want exactly 1 (the gap list)", len(msgs))
	}
	if msgs[0].Role != agent.RoleUser {
		t.Fatalf("seed message role = %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Text, "create_issue") {
		t.Fatalf("seed did not carry the gap: %q", msgs[0].Text)
	}
}

func TestModelProposerNeedsAStructuredAnswer(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "sure, I'll delete it"})
	r, err := agent.NewRunner(agent.RunnerConfig{Provider: stub})
	if err != nil {
		t.Fatal(err)
	}
	mp, _ := NewModelProposer(r)
	_, err = mp.Propose(context.Background(), []Gap{{Tool: "create_issue"}})
	if err == nil || !strings.Contains(err.Error(), "ProposalSchema") {
		t.Fatalf("err = %v, want a pointer at ProposalSchema", err)
	}
}

func TestModelProposerNoGapsNoCall(t *testing.T) {
	stub := agent.NewStubProvider(agent.StubTurn{Text: "{}"})
	r, _ := agent.NewRunner(agent.RunnerConfig{Provider: stub, ResponseSchema: ProposalSchema()})
	mp, _ := NewModelProposer(r)
	got, err := mp.Propose(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("got=%v err=%v, want no call and no proposals", got, err)
	}
	if len(stub.Requests()) != 0 {
		t.Fatalf("proposer called the model with nothing to propose")
	}
}

func TestNewModelProposerRejectsNilRunner(t *testing.T) {
	if _, err := NewModelProposer(nil); err == nil {
		t.Fatal("nil Runner should error")
	}
}
