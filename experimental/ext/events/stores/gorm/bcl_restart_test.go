package gormstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openSQLiteFile(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(path+"?_busy_timeout=5000"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// Issue 1441: a session revoked after a restart must still end the
// subscriptions it created, which needs the store to have kept the sid.
func TestBCL_TerminateBySessionAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events.db")

	store1, err := NewWebhookStore(openSQLiteFile(t, dbPath))
	require.NoError(t, err)
	r1 := events.NewWebhookRegistry(events.WithWebhookStore(store1), events.WithAllowInfiniteWebhookTTL())
	r1.Register(events.RegisterParams{
		CanonicalKey: []byte("bcl-key"), DerivedID: "sub_bcl", URL: "https://receiver.example/hook",
		Secret: "whsec_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EventName: "fake.event", Principal: "tenant/alice",
		Subject: "alice", SessionID: "sid-123", NoExpiry: true,
	})

	store2, err := NewWebhookStore(openSQLiteFile(t, dbPath))
	require.NoError(t, err)
	r2 := events.NewWebhookRegistry(events.WithWebhookStore(store2), events.WithAllowInfiniteWebhookTTL())

	n := r2.TerminateBySession("sid-123", events.ControlError{Code: -32012, Message: "session revoked"})
	assert.Equal(t, 1, n, "the restored subscription must match the revoked session")
	assert.Empty(t, r2.Targets())
}

// legacyWebhookRow is webhookRow as it stood before Subject and SessionID,
// so the test proves AutoMigrate adds those two to a populated table.
type legacyWebhookRow struct {
	CanonicalKey                   []byte `gorm:"primaryKey"`
	ID                             string `gorm:"not null"`
	URL                            string `gorm:"not null"`
	Secret                         string `gorm:"not null"`
	ExpiresAt                      *time.Time
	MaxAgeMs                       int            `gorm:"not null"`
	EventName                      string         `gorm:"not null;index:idx_webhooks_principal_event"`
	Principal                      string         `gorm:"not null;index:idx_webhooks_principal_event"`
	Arguments                      map[string]any `gorm:"serializer:json"`
	StatusActive                   bool           `gorm:"not null"`
	StatusLastDeliveryAt           *time.Time
	StatusLastError                string `gorm:"not null"`
	StatusFailedSince              *time.Time
	StatusThrottled                bool `gorm:"not null"`
	StatusRetryAfterMs             *int64
	StatusFailingContinuouslySince *time.Time
	FailureCount                   int `gorm:"not null"`
	VerifiedAt                     *time.Time
}

func (legacyWebhookRow) TableName() string { return "webhooks" }

func TestWebhookStore_AutoMigrateAddsSessionColumnsToPopulatedTable(t *testing.T) {
	db := openSQLiteFile(t, filepath.Join(t.TempDir(), "legacy.db"))
	require.NoError(t, db.AutoMigrate(&legacyWebhookRow{}))
	require.NoError(t, db.Create(&legacyWebhookRow{
		CanonicalKey: []byte("old"), ID: "sub_old", URL: "https://r.example/h", Secret: "whsec_x",
		EventName: "fake.event", Principal: "bob", StatusActive: true,
	}).Error)

	store, err := NewWebhookStore(db)
	require.NoError(t, err, "AutoMigrate must add the new columns to a table that already has rows")

	got, err := store.GetWebhook(t.Context(), events.GetWebhookRequest{CanonicalKey: []byte("old")})
	require.NoError(t, err)
	require.True(t, got.Found)
	assert.Equal(t, "sub_old", got.Target.ID)
	assert.Empty(t, got.Target.Subject)
	assert.Empty(t, got.Target.SessionID)
}
