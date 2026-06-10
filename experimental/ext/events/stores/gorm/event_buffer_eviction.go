package gormstore

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"
)

// EventBufferEvictionSweeper periodically drops expired rows from the
// event_buffer table. Run one per replica — the DELETE is idempotent
// and races between replicas are benign (the first to acquire the
// row lock wins; the second's WHERE clause filters out the already-
// deleted row).
//
// Usage:
//
//	sw := gormstore.NewEventBufferEvictionSweeper(db, 60 * time.Second)
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	go sw.Run(ctx)
//
// Run blocks until ctx is cancelled. Each tick logs the number of
// rows dropped (if any) via the configured Logger (default: log.Printf
// with the [gormstore.eviction] prefix).
type EventBufferEvictionSweeper struct {
	db       *gorm.DB
	interval time.Duration
	logf     func(format string, args ...any)
}

// NewEventBufferEvictionSweeper constructs a sweeper that fires every
// interval. Interval <=0 falls back to 60s — a balance between
// freshness (storage doesn't grow unbounded) and cost (one DELETE per
// minute per replica is negligible). Logger nil falls back to
// log.Printf.
func NewEventBufferEvictionSweeper(db *gorm.DB, interval time.Duration, opts ...EvictionOption) *EventBufferEvictionSweeper {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	sw := &EventBufferEvictionSweeper{db: db, interval: interval, logf: log.Printf}
	for _, o := range opts {
		o(sw)
	}
	return sw
}

// EvictionOption configures a sweeper.
type EvictionOption func(*EventBufferEvictionSweeper)

// WithEvictionLogger wires a custom logger. Default is log.Printf.
func WithEvictionLogger(f func(format string, args ...any)) EvictionOption {
	return func(s *EventBufferEvictionSweeper) {
		if f != nil {
			s.logf = f
		}
	}
}

// Run blocks until ctx is cancelled. Fires the eviction sweep at the
// configured interval. The sweep itself is a single DELETE WHERE
// expires_at < NOW() — Postgres index on expires_at keeps it cheap.
//
// On ctx cancellation, Run returns nil. Returns the last error only
// when the database is unrecoverable (closed connection, etc.); per-
// tick transient errors are logged and the loop continues.
func (s *EventBufferEvictionSweeper) Run(ctx context.Context) error {
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	s.runOnce(ctx) // immediate eviction at start — don't wait the first interval
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			s.runOnce(ctx)
		}
	}
}

// runOnce executes one eviction sweep. Errors are logged; the caller
// doesn't surface them so a transient hiccup doesn't kill the loop.
func (s *EventBufferEvictionSweeper) runOnce(ctx context.Context) {
	result := s.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&eventBufferRow{})
	if result.Error != nil {
		s.logf("[gormstore.eviction] sweep failed: %v", result.Error)
		return
	}
	if result.RowsAffected > 0 {
		s.logf("[gormstore.eviction] evicted %d expired rows", result.RowsAffected)
	}
}
