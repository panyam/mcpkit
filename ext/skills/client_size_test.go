package skills_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/panyam/mcpkit/ext/skills"
)

// Issue 867: the individual-file read path (resources/read, reached via
// ReadSkillURI and everything that flows through it) must bound a
// fetched resource's size before decode, and enforce a cumulative
// per-Client byte budget across a walk. These tests drive both caps
// against a real Provider-backed server.

const gitWorkflowURI = "skill://git-workflow/SKILL.md"

func TestClient_ReadSkillURI_RejectsOversized(t *testing.T) {
	// A cap smaller than the served SKILL.md must reject before decode.
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithMaxResourceBytes(16))
	_, err := sc.ReadSkillURI(context.Background(), gitWorkflowURI)
	if !errors.Is(err, skills.ErrResourceTooLarge) {
		t.Fatalf("ReadSkillURI err = %v, want ErrResourceTooLarge", err)
	}
}

func TestClient_ReadSkillURI_UnderCapSucceeds(t *testing.T) {
	// A generous cap leaves ordinary reads working.
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithMaxResourceBytes(1<<20))
	if _, err := sc.ReadSkillURI(context.Background(), gitWorkflowURI); err != nil {
		t.Fatalf("ReadSkillURI under cap: %v", err)
	}
}

func TestClient_MaxResourceBytes_NegativeDisablesCap(t *testing.T) {
	// -1 removes the cap entirely (even a byte-sized default would reject
	// nothing). Read the file to confirm the path stays open.
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithMaxResourceBytes(-1))
	if _, err := sc.ReadSkillURI(context.Background(), gitWorkflowURI); err != nil {
		t.Fatalf("ReadSkillURI with cap disabled: %v", err)
	}
}

func TestClient_DefaultCapAllowsOrdinaryReads(t *testing.T) {
	// The zero-option Client applies DefaultMaxResourceBytes, which must
	// not reject a normal SKILL.md.
	sc, _ := connectSkillsClient(t, "testdata/valid")
	if _, err := sc.ReadSkillURI(context.Background(), gitWorkflowURI); err != nil {
		t.Fatalf("ReadSkillURI under default cap: %v", err)
	}
}

func TestClient_ServerByteBudget_ExceededAcrossReads(t *testing.T) {
	data, err := os.ReadFile("testdata/valid/git-workflow/SKILL.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	n := int64(len(data))

	// Budget fits exactly one read but not two.
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithServerByteBudget(n+5))
	ctx := context.Background()

	if _, err := sc.ReadSkillURI(ctx, gitWorkflowURI); err != nil {
		t.Fatalf("first read within budget: %v", err)
	}
	if got := sc.BytesConsumed(); got != n {
		t.Fatalf("BytesConsumed after first read = %d, want %d", got, n)
	}

	// Second read would push the total to 2n, over the n+5 budget.
	_, err = sc.ReadSkillURI(ctx, gitWorkflowURI)
	if !errors.Is(err, skills.ErrServerByteBudgetExceeded) {
		t.Fatalf("second read err = %v, want ErrServerByteBudgetExceeded", err)
	}
	// A rejected read must not be charged.
	if got := sc.BytesConsumed(); got != n {
		t.Fatalf("BytesConsumed after rejected read = %d, want %d (rejection must not charge)", got, n)
	}
}

func TestClient_ServerByteBudget_DisabledByDefault(t *testing.T) {
	// With no budget option, BytesConsumed stays 0 and reads are
	// unbounded in aggregate.
	sc, _ := connectSkillsClient(t, "testdata/valid")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := sc.ReadSkillURI(ctx, gitWorkflowURI); err != nil {
			t.Fatalf("read %d with budget disabled: %v", i, err)
		}
	}
	if got := sc.BytesConsumed(); got != 0 {
		t.Fatalf("BytesConsumed with budget disabled = %d, want 0", got)
	}
}
