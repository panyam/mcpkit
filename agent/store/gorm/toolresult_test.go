package gormstore

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

func forEachToolResultBackend(t *testing.T, run func(t *testing.T, s *ToolResultStore, db *gorm.DB)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		db := openSQLite(t)
		run(t, newTRStore(t, db), db)
	})
	if dsn := postgresDSN(); dsn != "" {
		t.Run("postgres", func(t *testing.T) {
			db := openPostgres(t, dsn)
			run(t, newTRStore(t, db), db)
		})
	}
}

func newTRStore(t *testing.T, db *gorm.DB, opts ...ToolResultOption) *ToolResultStore {
	t.Helper()
	s, err := NewToolResultStore(db, opts...)
	if err != nil {
		t.Fatalf("NewToolResultStore: %v", err)
	}
	return s
}

func trText(s string) core.ToolResult {
	return core.ToolResult{Content: []core.Content{{Type: "text", Text: s}}}
}

func TestGormToolResultStore_PutGet(t *testing.T) {
	forEachToolResultBackend(t, func(t *testing.T, s *ToolResultStore, _ *gorm.DB) {
		ctx := context.Background()
		if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:a", Result: trText("payload")}); err != nil {
			t.Fatalf("PutToolResult: %v", err)
		}
		resp, err := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:a"})
		if err != nil || !resp.Found {
			t.Fatalf("GetToolResult = (%+v, %v)", resp, err)
		}
		if resp.Result.Content[0].Text != "payload" {
			t.Fatalf("round-trip lost content: %+v", resp.Result)
		}
	})
}

func TestGormToolResultStore_UnknownRefIsAppState(t *testing.T) {
	forEachToolResultBackend(t, func(t *testing.T, s *ToolResultStore, _ *gorm.DB) {
		resp, err := s.GetToolResult(context.Background(), agent.GetToolResultRequest{Ref: "res:gone"})
		if err != nil || resp.Found {
			t.Fatalf("Get(unknown) = (%+v, %v), want Found=false, nil error", resp, err)
		}
	})
}

func TestGormToolResultStore_Upsert(t *testing.T) {
	forEachToolResultBackend(t, func(t *testing.T, s *ToolResultStore, _ *gorm.DB) {
		ctx := context.Background()
		for _, v := range []string{"first", "second"} {
			if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:x", Result: trText(v)}); err != nil {
				t.Fatalf("Put %q: %v", v, err)
			}
		}
		resp, _ := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:x"})
		if resp.Result.Content[0].Text != "second" {
			t.Fatalf("upsert did not overwrite: %+v", resp.Result)
		}
	})
}

func TestGormToolResultStore_PruneExpired(t *testing.T) {
	forEachToolResultBackend(t, func(t *testing.T, s *ToolResultStore, db *gorm.DB) {
		ctx := context.Background()
		// no retention: prune is a no-op even for old rows
		if n, err := s.PruneExpired(ctx); err != nil || n != 0 {
			t.Fatalf("PruneExpired without retention = (%d, %v), want (0, nil)", n, err)
		}

		rs := newTRStore(t, db, WithToolResultRetention(time.Hour))
		if _, err := rs.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:old", Result: trText("stale")}); err != nil {
			t.Fatalf("PutToolResult: %v", err)
		}
		if _, err := rs.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:new", Result: trText("fresh")}); err != nil {
			t.Fatalf("PutToolResult: %v", err)
		}
		// backdate one row past the window
		if err := db.Table(DefaultToolResultTable).Where("ref = ?", "res:old").
			Update("stored_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
			t.Fatalf("backdating: %v", err)
		}

		n, err := rs.PruneExpired(ctx)
		if err != nil || n != 1 {
			t.Fatalf("PruneExpired = (%d, %v), want (1, nil)", n, err)
		}
		if resp, _ := rs.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:old"}); resp.Found {
			t.Fatal("expired row survived prune")
		}
		if resp, _ := rs.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:new"}); !resp.Found {
			t.Fatal("in-window row was pruned")
		}
	})
}

func TestGormToolResultStore_CustomTableName(t *testing.T) {
	forEachToolResultBackend(t, func(t *testing.T, _ *ToolResultStore, db *gorm.DB) {
		ctx := context.Background()
		s := newTRStore(t, db, WithToolResultTableName("custom_results"))
		if _, err := s.PutToolResult(ctx, agent.PutToolResultRequest{Ref: "res:c", Result: trText("in custom table")}); err != nil {
			t.Fatalf("PutToolResult: %v", err)
		}
		// the default-table store must NOT see it — proves the row landed
		// in the custom table
		def := newTRStore(t, db)
		if resp, _ := def.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:c"}); resp.Found {
			t.Fatal("row written to custom table was visible in the default table")
		}
		if resp, _ := s.GetToolResult(ctx, agent.GetToolResultRequest{Ref: "res:c"}); !resp.Found {
			t.Fatal("custom-table store could not read its own row")
		}
	})
}
