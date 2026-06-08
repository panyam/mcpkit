package gormstore

import (
	"context"
	"testing"
	"time"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mkTarget(canonicalKey []byte, principal, eventName string) events.WebhookTarget {
	return events.WebhookTarget{
		CanonicalKey:  canonicalKey,
		ID:            "sub_" + principal + "_" + eventName,
		URL:           "https://example.test/webhook",
		Secret:        "whsec_test",
		ExpiresAt:     time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		MaxAgeSeconds: 60,
		EventName:     eventName,
		Principal:     principal,
		Params:        map[string]any{"channel": "general", "n": float64(7)},
		Status: events.DeliveryStatus{
			Active:    true,
			LastError: events.DeliveryErrorNone,
		},
		FailureCount: 0,
	}
}

func TestWebhookStore_GetSaveDeleteCount(t *testing.T) {
	for _, bk := range backends(t) {
		bk := bk
		t.Run(bk.name, func(t *testing.T) {
			store := bk.newWebhookStore(t)
			ctx := context.Background()

			// Empty store
			getResp, err := store.GetWebhook(ctx, events.GetWebhookRequest{CanonicalKey: []byte("missing")})
			require.NoError(t, err)
			assert.False(t, getResp.Found)

			countResp, err := store.CountWebhooks(ctx, events.CountWebhooksRequest{})
			require.NoError(t, err)
			assert.Equal(t, 0, countResp.Count)

			// Save one, get it back
			key := []byte("ck-1")
			target := mkTarget(key, "alice", "chat.message")
			_, err = store.SaveWebhook(ctx, events.SaveWebhookRequest{Target: target})
			require.NoError(t, err)

			getResp, err = store.GetWebhook(ctx, events.GetWebhookRequest{CanonicalKey: key})
			require.NoError(t, err)
			require.True(t, getResp.Found)
			assert.Equal(t, target.ID, getResp.Target.ID)
			assert.Equal(t, target.URL, getResp.Target.URL)
			assert.Equal(t, target.Principal, getResp.Target.Principal)
			assert.Equal(t, target.Params["channel"], getResp.Target.Params["channel"])
			assert.True(t, target.ExpiresAt.Equal(getResp.Target.ExpiresAt))

			// Update via Save overwrites; FailureCount round-trips
			target.FailureCount = 3
			target.Status.Active = false
			target.Status.LastError = events.DeliveryError5xx
			fs := time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC)
			target.Status.FailedSince = &fs
			_, err = store.SaveWebhook(ctx, events.SaveWebhookRequest{Target: target})
			require.NoError(t, err)
			getResp, err = store.GetWebhook(ctx, events.GetWebhookRequest{CanonicalKey: key})
			require.NoError(t, err)
			require.True(t, getResp.Found)
			assert.Equal(t, 3, getResp.Target.FailureCount)
			assert.False(t, getResp.Target.Status.Active)
			assert.Equal(t, events.DeliveryError5xx, getResp.Target.Status.LastError)
			require.NotNil(t, getResp.Target.Status.FailedSince)
			assert.True(t, fs.Equal(*getResp.Target.Status.FailedSince))

			// Count reflects one row
			countResp, err = store.CountWebhooks(ctx, events.CountWebhooksRequest{})
			require.NoError(t, err)
			assert.Equal(t, 1, countResp.Count)

			// Delete returns the removed row
			delResp, err := store.DeleteWebhook(ctx, events.DeleteWebhookRequest{CanonicalKey: key})
			require.NoError(t, err)
			require.True(t, delResp.Found)
			assert.Equal(t, target.URL, delResp.Removed.URL)

			// Second delete is a no-op
			delResp, err = store.DeleteWebhook(ctx, events.DeleteWebhookRequest{CanonicalKey: key})
			require.NoError(t, err)
			assert.False(t, delResp.Found)
		})
	}
}

func TestWebhookStore_ListReturnsSnapshot(t *testing.T) {
	for _, bk := range backends(t) {
		bk := bk
		t.Run(bk.name, func(t *testing.T) {
			store := bk.newWebhookStore(t)
			ctx := context.Background()

			ids := []string{"a", "b", "c"}
			for _, id := range ids {
				key := []byte("list-" + id)
				_, err := store.SaveWebhook(ctx, events.SaveWebhookRequest{Target: mkTarget(key, id, "evt")})
				require.NoError(t, err)
			}

			listResp, err := store.ListWebhooks(ctx, events.ListWebhooksRequest{})
			require.NoError(t, err)
			assert.Equal(t, len(ids), len(listResp.Targets))

			// Mutating the returned slice must not affect the store.
			listResp.Targets = listResp.Targets[:0]
			countResp, err := store.CountWebhooks(ctx, events.CountWebhooksRequest{})
			require.NoError(t, err)
			assert.Equal(t, len(ids), countResp.Count)
		})
	}
}
