package gormstore

import (
	"fmt"
	"os"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// backend wraps a (name, store-factory) pair so conformance tests can
// run the same assertion body against every available backend without
// duplicating the matrix at every test site.
type backend struct {
	name           string
	newWebhookStore func(t *testing.T) events.WebhookStore
	newQuotaStore   func(t *testing.T) events.QuotaStore
}

// backends returns the list of backends to test against this run. The
// in-memory backend always runs (it is the contract reference). SQLite
// runs by default (no Docker required). Postgres runs only when
// MCPKIT_EVENTS_TEST_PGDB and related env vars are set — `make testpg`
// in this directory boots a container and exports them.
func backends(t *testing.T) []backend {
	t.Helper()
	out := []backend{
		{
			name:            "inmemory",
			newWebhookStore: func(_ *testing.T) events.WebhookStore { return events.NewInMemoryWebhookStore() },
			newQuotaStore:   func(_ *testing.T) events.QuotaStore { return events.NewInMemoryQuotaStore() },
		},
		{
			name:            "sqlite",
			newWebhookStore: func(t *testing.T) events.WebhookStore { return newWebhookStoreFromDB(t, openSQLite(t)) },
			newQuotaStore:   func(t *testing.T) events.QuotaStore { return newQuotaStoreFromDB(t, openSQLite(t)) },
		},
	}
	if pgDSN := postgresDSN(); pgDSN != "" {
		out = append(out, backend{
			name:            "postgres",
			newWebhookStore: func(t *testing.T) events.WebhookStore { return newWebhookStoreFromDB(t, openPostgres(t, pgDSN)) },
			newQuotaStore:   func(t *testing.T) events.QuotaStore { return newQuotaStoreFromDB(t, openPostgres(t, pgDSN)) },
		})
	}
	return out
}

// openSQLite opens a fresh in-process SQLite database. Each call gets
// its own private :memory: instance so subtests don't share rows.
// MaxOpenConns(1) avoids "database is locked" errors under the
// concurrent-reserve test — SQLite serializes writers, so multiple
// connections racing on the same in-memory DB collide. One pooled
// connection per *gorm.DB keeps the test honest about CAS semantics
// without fighting SQLite's locking model. Production SQLite users
// either accept this serialization or use WAL mode on a file-backed
// database; the demo target is Postgres regardless.
func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("sqlite open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite raw DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// postgresDSN returns the connection string assembled from
// MCPKIT_EVENTS_TEST_PG* env vars, or "" when any required var is
// missing. Used to gate the postgres backend in the conformance matrix.
func postgresDSN() string {
	host := getenv("MCPKIT_EVENTS_TEST_PGHOST", "localhost")
	port := getenv("MCPKIT_EVENTS_TEST_PGPORT", "5434")
	user := os.Getenv("MCPKIT_EVENTS_TEST_PGUSER")
	pass := os.Getenv("MCPKIT_EVENTS_TEST_PGPASSWORD")
	dbname := os.Getenv("MCPKIT_EVENTS_TEST_PGDB")
	if user == "" || pass == "" || dbname == "" {
		return ""
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable", host, port, user, pass, dbname)
}

func openPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("postgres open: %v", err)
	}
	// Ensure tables exist before truncating — first test in the run
	// would fail without this.
	if err := db.AutoMigrate(&webhookRow{}, &quotaRow{}); err != nil {
		t.Fatalf("postgres automigrate: %v", err)
	}
	// Reset state BEFORE the test runs so leftover rows from prior
	// subtests don't poison this one. Postgres tests share one
	// container / one database across subtests, unlike SQLite's
	// per-test :memory: instance.
	if err := db.Exec("TRUNCATE TABLE webhooks, quota_counters").Error; err != nil {
		t.Fatalf("postgres truncate: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newWebhookStoreFromDB(t *testing.T, db *gorm.DB) events.WebhookStore {
	t.Helper()
	s, err := NewWebhookStore(db)
	if err != nil {
		t.Fatalf("NewWebhookStore: %v", err)
	}
	return s
}

func newQuotaStoreFromDB(t *testing.T, db *gorm.DB) events.QuotaStore {
	t.Helper()
	s, err := NewQuotaStore(db)
	if err != nil {
		t.Fatalf("NewQuotaStore: %v", err)
	}
	return s
}

func getenv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
