package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
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

func TestBuildToolResultStoreSpecs(t *testing.T) {
	// memory / empty -> nil (host uses its in-memory default)
	for _, spec := range []string{"", "memory"} {
		if s, err := buildToolResultStore(spec); err != nil || s != nil {
			t.Fatalf("buildToolResultStore(%q) = (%v, %v), want (nil, nil)", spec, s, err)
		}
	}
	for _, bad := range []string{"bogus", "sqlite://", "redis://"} {
		if _, err := buildToolResultStore(bad); err == nil {
			t.Fatalf("buildToolResultStore(%q) succeeded, want error", bad)
		}
	}
}

// TestBuildToolResultStoreSQLite pins that a sqlite spec yields a usable
// durable blob store on a local file — the no-server offload path.
func TestBuildToolResultStoreSQLite(t *testing.T) {
	spec := "sqlite://" + filepath.Join(t.TempDir(), "blobs.db")
	s, err := buildToolResultStore(spec)
	if err != nil || s == nil {
		t.Fatalf("buildToolResultStore(sqlite) = (%v, %v)", s, err)
	}
	ctx := context.Background()
	if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{
		Ref:    "res:a",
		Result: core.ToolResult{Content: []core.Content{{Type: "text", Text: "payload"}}},
	}); err != nil {
		t.Fatalf("PutToolResult: %v", err)
	}
	resp, err := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"})
	if err != nil || !resp.Found {
		t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
	}
}
