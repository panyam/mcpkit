package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/panyam/mcpkit/experimental/ext/events"
	gormstore "github.com/panyam/mcpkit/experimental/ext/events/stores/gorm"
)

// postgresBackend bundles the Postgres-backed WebhookStore plumbing so
// main.go can wire it via one call site + one defer. configurePostgresBackend
// activates the backend when POSTGRES_DSN is set; otherwise it returns a
// no-op handle so main.go's defer becomes harmless and the events lib
// uses the in-memory WebhookStore default.
//
// The in-memory default works for single-replica demos but loses every
// subscription on event-server restart. Switching to the Postgres-backed
// store lets webhook subscriptions survive replica churn — load-bearing
// for the multi-replica stage-3 walkthrough where one replica goes
// away mid-stream and another picks up its subscriptions.
type postgresBackend struct {
	db          *gorm.DB
	store       events.WebhookStore
	bufferStore events.EventBufferStore
	sweeper     *gormstore.EventBufferEvictionSweeper
	sweeperStop context.CancelFunc
	sweeperDone chan struct{}
}

// webhookStore returns the Postgres-backed WebhookStore, or nil when
// POSTGRES_DSN was empty. Callers should check for nil before applying
// the option.
func (p *postgresBackend) webhookStore() events.WebhookStore {
	if p == nil {
		return nil
	}
	return p.store
}

// eventBufferStore returns the Postgres-backed EventBufferStore for use
// in events.WithEventBufferStore(...) on YieldingSource construction.
// nil when POSTGRES_DSN was empty (sources fall back to the in-memory
// ring default — single-replica behavior).
func (p *postgresBackend) eventBufferStore() events.EventBufferStore {
	if p == nil {
		return nil
	}
	return p.bufferStore
}

func (p *postgresBackend) shutdown() {
	if p == nil {
		return
	}
	if p.sweeperStop != nil {
		p.sweeperStop()
		<-p.sweeperDone
	}
	if p.db != nil {
		if sqlDB, err := p.db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}
}

// configurePostgresBackend opens the Postgres database referenced by
// POSTGRES_DSN, constructs the gorm-backed WebhookStore, and returns a
// non-nil *postgresBackend so the caller can defer shutdown unconditionally.
// When POSTGRES_DSN is empty, returns a zero handle (webhookStore() → nil).
//
// Recognized env vars:
//
//	POSTGRES_DSN  Required to activate. Standard libpq URL form:
//	              postgres://<user>:<pass>@<host>:<port>/<db>?sslmode=...
//	              Empty leaves the in-memory WebhookStore default in place.
//
// The store calls AutoMigrate on construction by default; the gorm
// sub-module's WithoutAutoMigrate option (or an out-of-band migration
// tool) is the production path. The demo runs against a fresh
// container each `make up`, so AutoMigrate is the right call here.
func configurePostgresBackend() *postgresBackend {
	dsn := strings.TrimSpace(os.Getenv("POSTGRES_DSN"))
	if dsn == "" {
		log.Printf("[event-server] Postgres backend: disabled (POSTGRES_DSN empty); using in-memory WebhookStore")
		return &postgresBackend{}
	}

	// Wait up to ~30s for Postgres to be ready before failing. Compose
	// healthchecks gate `event-server` on `postgres: service_healthy`, but
	// in dev environments without that guard the connection can race
	// the database's first-accept window.
	db, err := openWithRetry(dsn, 30*time.Second)
	if err != nil {
		log.Fatalf("postgres open failed: %v", err)
	}

	store, err := gormstore.NewWebhookStore(db)
	if err != nil {
		log.Fatalf("gormstore.NewWebhookStore: %v", err)
	}

	// Event buffer store (issue 727) — backs YieldingSource's poll
	// buffer with Postgres so multi-replica deployments answer Poll
	// consistently. TTL is parsed from POSTGRES_BUFFER_TTL (Go
	// duration, e.g., "10m"); falls back to the gorm sub-module's
	// default (1h).
	bufferOpts := []gormstore.Option{}
	if rawTTL := strings.TrimSpace(os.Getenv("POSTGRES_BUFFER_TTL")); rawTTL != "" {
		if ttl, err := time.ParseDuration(rawTTL); err == nil && ttl > 0 {
			bufferOpts = append(bufferOpts, gormstore.WithBufferTTL(ttl))
		}
	}
	bufferStore, err := gormstore.NewEventBufferStore(db, bufferOpts...)
	if err != nil {
		log.Fatalf("gormstore.NewEventBufferStore: %v", err)
	}

	// Background eviction sweeper — fires every 60s; logs evictions.
	// One per replica; the DELETE is idempotent so races between
	// replicas don't matter.
	sweeper := gormstore.NewEventBufferEvictionSweeper(db, 60*time.Second)
	sweeperCtx, sweeperStop := context.WithCancel(context.Background())
	sweeperDone := make(chan struct{})
	go func() {
		defer close(sweeperDone)
		_ = sweeper.Run(sweeperCtx)
	}()

	log.Printf("[event-server] Postgres backend active: WebhookStore + EventBufferStore via gormstore (DSN host=%s)",
		hostFromDSN(dsn))

	return &postgresBackend{
		db:          db,
		store:       store,
		bufferStore: bufferStore,
		sweeper:     sweeper,
		sweeperStop: sweeperStop,
		sweeperDone: sweeperDone,
	}
}

// openWithRetry retries gorm.Open until the database accepts a Ping or
// the deadline elapses. Returns the live *gorm.DB on success.
func openWithRetry(dsn string, deadline time.Duration) (*gorm.DB, error) {
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: gormlogger.Default.LogMode(gormlogger.Silent),
		})
		if err == nil {
			sqlDB, pingErr := db.DB()
			if pingErr == nil {
				if pingErr = sqlDB.PingContext(context.Background()); pingErr == nil {
					return db, nil
				}
				lastErr = pingErr
			} else {
				lastErr = pingErr
			}
		} else {
			lastErr = err
		}
		time.Sleep(1 * time.Second)
	}
	return nil, lastErr
}

// hostFromDSN extracts the host[:port] portion of a Postgres DSN for
// log output. Tolerates malformed input by returning the raw DSN with
// embedded credentials stripped.
func hostFromDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i >= 0 {
		dsn = dsn[i+1:]
	}
	if i := strings.Index(dsn, "/"); i >= 0 {
		dsn = dsn[:i]
	}
	if i := strings.Index(dsn, "?"); i >= 0 {
		dsn = dsn[:i]
	}
	return dsn
}
