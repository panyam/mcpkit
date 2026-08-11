package surfaces

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/panyam/mcpkit/experimental/agent"
)

// TestRunStoreFromSpec covers the spec-string mapping: the no-op empty
// spec, the in-memory backend, and the malformed specs that must error.
func TestRunStoreFromSpec(t *testing.T) {
	if s, err := RunStoreFromSpec(""); err != nil || s != nil {
		t.Fatalf(`RunStoreFromSpec("") = (%v, %v), want (nil, nil)`, s, err)
	}
	if s, err := RunStoreFromSpec("memory"); err != nil || s == nil {
		t.Fatalf("RunStoreFromSpec(memory) = (%v, %v), want a store", s, err)
	}
	for _, bad := range []string{"bogus", "sqlite://", "redis://"} {
		if _, err := RunStoreFromSpec(bad); err == nil {
			t.Fatalf("RunStoreFromSpec(%q) succeeded, want error", bad)
		}
	}
}

// TestRunStoreFromSpecSQLiteSurvivesReopen pins the no-server flow: a
// sqlite:// session store persists runs across process restarts with
// nothing but a local file.
func TestRunStoreFromSpecSQLiteSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	spec := "sqlite://" + filepath.Join(t.TempDir(), "sessions.db")

	s1, err := RunStoreFromSpec(spec)
	if err != nil {
		t.Fatalf("RunStoreFromSpec: %v", err)
	}
	created, err := s1.CreateRun(ctx, agent.CreateRunRequest{RunID: "standup"})
	if err != nil || !created.Created {
		t.Fatalf("CreateRun = (%+v, %v)", created, err)
	}
	if _, err := s1.AppendMessages(ctx, agent.AppendMessagesRequest{
		RunID: "standup", Messages: []agent.Message{{Role: agent.RoleUser, Text: "persisted"}},
	}); err != nil {
		t.Fatalf("AppendMessages: %v", err)
	}

	s2, err := RunStoreFromSpec(spec)
	if err != nil {
		t.Fatalf("RunStoreFromSpec(reopen): %v", err)
	}
	resp, err := s2.LoadRun(ctx, agent.LoadRunRequest{RunID: "standup"})
	if err != nil || !resp.Found {
		t.Fatalf("LoadRun after reopen = (%+v, %v)", resp, err)
	}
	if len(resp.Run.Messages) != 1 || resp.Run.Messages[0].Text != "persisted" {
		t.Fatalf("session did not survive reopen: %+v", resp.Run.Messages)
	}
}
