package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/panyam/mcpkit/agent"
)

func TestBuildRunStoreSpecs(t *testing.T) {
	if s, err := buildRunStore(""); err != nil || s != nil {
		t.Fatalf("buildRunStore(\"\") = (%v, %v), want (nil, nil)", s, err)
	}
	if s, err := buildRunStore("memory"); err != nil || s == nil {
		t.Fatalf("buildRunStore(memory) = (%v, %v)", s, err)
	}
	for _, bad := range []string{"bogus", "sqlite://", "redis://"} {
		if _, err := buildRunStore(bad); err == nil {
			t.Fatalf("buildRunStore(%q) succeeded, want error", bad)
		}
	}
}

// TestBuildRunStoreSQLiteSurvivesReopen pins the no-server flow: a
// sqlite:// session store persists runs across process restarts with
// nothing but a local file.
func TestBuildRunStoreSQLiteSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	spec := "sqlite://" + filepath.Join(t.TempDir(), "sessions.db")

	s1, err := buildRunStore(spec)
	if err != nil {
		t.Fatalf("buildRunStore: %v", err)
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

	s2, err := buildRunStore(spec)
	if err != nil {
		t.Fatalf("buildRunStore(reopen): %v", err)
	}
	resp, err := s2.LoadRun(ctx, agent.LoadRunRequest{RunID: "standup"})
	if err != nil || !resp.Found {
		t.Fatalf("LoadRun after reopen = (%+v, %v)", resp, err)
	}
	if len(resp.Run.Messages) != 1 || resp.Run.Messages[0].Text != "persisted" {
		t.Fatalf("session did not survive reopen: %+v", resp.Run.Messages)
	}
}
