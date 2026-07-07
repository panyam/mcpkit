package gormstore

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuotaStore_ReserveReleaseCount(t *testing.T) {
	for _, bk := range backends(t) {
		bk := bk
		t.Run(bk.name, func(t *testing.T) {
			store := bk.newQuotaStore(t)
			ctx := context.Background()

			cntResp, err := store.CountQuota(ctx, events.CountQuotaRequest{Principal: "alice", Key: "chat"})
			require.NoError(t, err)
			assert.Equal(t, 0, cntResp.Count)

			// Reserve up to Max
			for i := 1; i <= 3; i++ {
				resp, err := store.ReserveQuota(ctx, events.ReserveQuotaRequest{Principal: "alice", Key: "chat", Max: 3})
				require.NoError(t, err)
				assert.True(t, resp.Granted, "reservation %d should be granted", i)
				assert.Equal(t, i, resp.Count)
			}

			// Fourth reservation must be denied
			resp, err := store.ReserveQuota(ctx, events.ReserveQuotaRequest{Principal: "alice", Key: "chat", Max: 3})
			require.NoError(t, err)
			assert.False(t, resp.Granted)
			assert.Equal(t, 3, resp.Count)

			// Release returns a slot
			_, err = store.ReleaseQuota(ctx, events.ReleaseQuotaRequest{Principal: "alice", Key: "chat"})
			require.NoError(t, err)
			cntResp, err = store.CountQuota(ctx, events.CountQuotaRequest{Principal: "alice", Key: "chat"})
			require.NoError(t, err)
			assert.Equal(t, 2, cntResp.Count)

			// Now reservation works again
			resp, err = store.ReserveQuota(ctx, events.ReserveQuotaRequest{Principal: "alice", Key: "chat", Max: 3})
			require.NoError(t, err)
			assert.True(t, resp.Granted)
			assert.Equal(t, 3, resp.Count)

			// Different (principal, eventName) is independent
			resp, err = store.ReserveQuota(ctx, events.ReserveQuotaRequest{Principal: "bob", Key: "chat", Max: 3})
			require.NoError(t, err)
			assert.True(t, resp.Granted)
			assert.Equal(t, 1, resp.Count)

			// Release at zero is silent no-op (matches in-memory contract)
			for i := 0; i < 10; i++ {
				_, err = store.ReleaseQuota(ctx, events.ReleaseQuotaRequest{Principal: "carol", Key: "chat"})
				require.NoError(t, err)
			}
			cntResp, err = store.CountQuota(ctx, events.CountQuotaRequest{Principal: "carol", Key: "chat"})
			require.NoError(t, err)
			assert.Equal(t, 0, cntResp.Count)
		})
	}
}

// TestQuotaStore_ConcurrentReserveAtomicity verifies the CAS contract:
// when N goroutines race to reserve against Max=K for the same
// (principal, eventName), EXACTLY K reservations succeed. This is the
// raison d'être of the seam — a multi-replica store must enforce the
// invariant from the database.
//
// The in-memory store is intentionally skipped: its contract is
// "Quota wrapper holds a mutex around every call" — when callers race
// the store directly, the panicking behavior is by design and is the
// reason persistent backends exist in the first place.
func TestQuotaStore_ConcurrentReserveAtomicity(t *testing.T) {
	for _, bk := range backends(t) {
		bk := bk
		if bk.name == "inmemory" {
			continue
		}
		t.Run(bk.name, func(t *testing.T) {
			store := bk.newQuotaStore(t)
			ctx := context.Background()

			const goroutines = 30
			const max = 5

			var granted atomic.Int64
			var wg sync.WaitGroup
			wg.Add(goroutines)
			for i := 0; i < goroutines; i++ {
				go func() {
					defer wg.Done()
					resp, err := store.ReserveQuota(ctx, events.ReserveQuotaRequest{
						Principal: "alice", Key: "chat", Max: max,
					})
					if err != nil {
						t.Errorf("ReserveQuota: %v", err)
						return
					}
					if resp.Granted {
						granted.Add(1)
					}
				}()
			}
			wg.Wait()

			assert.Equal(t, int64(max), granted.Load(),
				"expected exactly %d grants under contention, got %d", max, granted.Load())

			cntResp, err := store.CountQuota(ctx, events.CountQuotaRequest{Principal: "alice", Key: "chat"})
			require.NoError(t, err)
			assert.Equal(t, max, cntResp.Count)
		})
	}
}
