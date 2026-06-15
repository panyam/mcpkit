package gormstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestNoExpirySurvival_RestartSurvivalThroughGORMSQLite is the
// load-bearing integration test for issue 764. It exercises the
// end-to-end durability contract spec PR1 commit 99f3589c
// §"Subscription TTL" attaches to no-expiry grants: persistence
// across process restart.
//
// The shape of this test is deliberately designed to be the
// precursor of a future whole-enchilada walkthrough beat — same
// fixture pattern, same store seam, just SQLite locally vs Postgres
// in the demo. The expected runbook is:
//
//  1. Open the persistent backend (SQLite file here; Postgres in
//     whole-enchilada).
//  2. Build a registry with WithAllowInfiniteWebhookTTL +
//     WithWebhookStore(persistent).
//  3. Register a no-expiry subscription.
//  4. Tear down the registry, mimicking a process restart (drop
//     in-memory state, close + reopen the DB).
//  5. Build a fresh registry pointing at the same backend.
//  6. Verify the subscription is still there AND deliveries route to
//     the correct receiver.
//
// When the no-expiry-survival walkthrough beat lands on the
// whole-enchilada, it should literally script these six steps,
// swapping in `docker compose restart event-server` for the close-
// and-reopen pair.
func TestNoExpirySurvival_RestartSurvivalThroughGORMSQLite(t *testing.T) {
	// SQLite file in a temp dir survives the close-and-reopen pair.
	// (In-memory `file::memory:` does NOT survive a Close; we
	// explicitly need on-disk persistence here.)
	dbPath := filepath.Join(t.TempDir(), "events.db")

	openStore := func() (events.WebhookStore, func()) {
		db, err := gorm.Open(sqlite.Open(dbPath+"?_busy_timeout=5000"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		store, err := NewWebhookStore(db)
		require.NoError(t, err)
		return store, func() { _ = sqlDB.Close() }
	}

	// Set up a real httptest receiver so step 6 (post-restart
	// delivery) is verifiable end-to-end.
	var receivedAfterRestart atomic.Bool
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receivedAfterRestart.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	canonicalKey := []byte("survival-test-key")
	derivedID := "sub_survival"
	const principal = "alice"
	const eventName = "fake.event"

	// Phase 1: original "process" — register the no-expiry sub.
	{
		store, closeDB := openStore()
		r := events.NewWebhookRegistry(
			events.WithWebhookAllowPrivateNetworks(true),
			events.WithAllowInfiniteWebhookTTL(),
			events.WithWebhookStore(store),
		)
		_, isNew := r.Register(events.RegisterParams{
			CanonicalKey: canonicalKey,
			DerivedID:    derivedID,
			URL:          receiver.URL,
			Secret:       "whsec_" + strings.Repeat("a", 32),
			EventName:    eventName,
			Principal:    principal,
			NoExpiry:     true,
		})
		require.True(t, isNew, "first Register MUST report isNew=true")

		// Verify in this process: target is live and ExpiresAt is nil.
		liveTargets := r.Targets()
		require.Len(t, liveTargets, 1, "no-expiry sub MUST show in Targets() in the original process")
		assert.Nil(t, liveTargets[0].ExpiresAt, "ExpiresAt MUST be nil for no-expiry sub")

		closeDB()
	}

	// Phase 2: fresh "process" — open the same DB, build a new
	// registry, verify the sub survived. Includes DeliverToTarget
	// proving the survived sub can route real deliveries.
	{
		store, closeDB := openStore()
		defer closeDB()
		r := events.NewWebhookRegistry(
			events.WithWebhookAllowPrivateNetworks(true),
			events.WithAllowInfiniteWebhookTTL(),
			events.WithWebhookStore(store),
		)

		liveTargets := r.Targets()
		require.Len(t, liveTargets, 1,
			"no-expiry sub MUST survive process restart (got %d targets after restart)", len(liveTargets))
		got := liveTargets[0]
		assert.Equal(t, derivedID, got.ID, "DerivedID MUST round-trip across restart")
		assert.Equal(t, receiver.URL, got.URL, "URL MUST round-trip across restart")
		assert.Equal(t, principal, got.Principal, "Principal MUST round-trip across restart")
		assert.Nil(t, got.ExpiresAt,
			"ExpiresAt MUST round-trip as nil — restart MUST NOT silently rehydrate a finite expiry on a no-expiry sub")

		// Drive a real delivery to verify the post-restart registry
		// can actually route to the survived sub. DeliverToTarget
		// fires the POST in a goroutine.
		ok := r.DeliverToTarget(
			context.Background(),
			canonicalKey,
			events.MakeEvent(eventName, "evt_after_restart", "1", time.Now(), map[string]string{"k": "v"}),
		)
		require.True(t, ok, "DeliverToTarget MUST find the survived target")
		require.Eventually(t, receivedAfterRestart.Load, 2*time.Second, 20*time.Millisecond,
			"a no-expiry sub that survived a restart MUST be routable to its receiver")
	}
}
