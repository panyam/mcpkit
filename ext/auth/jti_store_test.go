package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryJTIStore_RecordAndSeen(t *testing.T) {
	s := NewMemoryJTIStore()
	ctx := context.Background()

	resp, err := s.Seen(ctx, SeenRequest{JTI: "a"})
	require.NoError(t, err)
	assert.False(t, resp.Found, "fresh jti must not be Found")

	_, err = s.Record(ctx, RecordRequest{JTI: "a", TTL: time.Hour})
	require.NoError(t, err)

	resp, err = s.Seen(ctx, SeenRequest{JTI: "a"})
	require.NoError(t, err)
	assert.True(t, resp.Found, "recorded jti must be Found")
}

func TestMemoryJTIStore_EvictsExpired(t *testing.T) {
	s := NewMemoryJTIStore()
	clock := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	ctx := context.Background()

	_, err := s.Record(ctx, RecordRequest{JTI: "expiring", TTL: 10 * time.Second})
	require.NoError(t, err)

	clock = clock.Add(5 * time.Second)
	resp, err := s.Seen(ctx, SeenRequest{JTI: "expiring"})
	require.NoError(t, err)
	assert.True(t, resp.Found, "within TTL window: still Found")

	clock = clock.Add(10 * time.Second) // 15s total, past 10s TTL
	resp, err = s.Seen(ctx, SeenRequest{JTI: "expiring"})
	require.NoError(t, err)
	assert.False(t, resp.Found, "past TTL: must be evicted")
}

func TestMemoryJTIStore_ReRecordResetsTTL(t *testing.T) {
	s := NewMemoryJTIStore()
	clock := time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	ctx := context.Background()

	_, _ = s.Record(ctx, RecordRequest{JTI: "a", TTL: 10 * time.Second})
	clock = clock.Add(9 * time.Second)
	_, _ = s.Record(ctx, RecordRequest{JTI: "a", TTL: 10 * time.Second})

	// 12s after original record, but only 3s after re-record — must still be Found.
	clock = clock.Add(3 * time.Second)
	resp, _ := s.Seen(ctx, SeenRequest{JTI: "a"})
	assert.True(t, resp.Found, "re-record must extend the TTL window")
}

func TestMemoryJTIStore_EmptyJTIIsNoop(t *testing.T) {
	s := NewMemoryJTIStore()
	ctx := context.Background()

	resp, err := s.Seen(ctx, SeenRequest{JTI: ""})
	require.NoError(t, err)
	assert.False(t, resp.Found, "empty jti reports not Found rather than panic")

	_, err = s.Record(ctx, RecordRequest{JTI: "", TTL: time.Hour})
	require.NoError(t, err, "empty jti record is a no-op, not an error")
}

func TestMemoryJTIStore_ZeroTTLIsNoop(t *testing.T) {
	s := NewMemoryJTIStore()
	ctx := context.Background()
	_, err := s.Record(ctx, RecordRequest{JTI: "x", TTL: 0})
	require.NoError(t, err)
	resp, _ := s.Seen(ctx, SeenRequest{JTI: "x"})
	assert.False(t, resp.Found, "TTL=0 must not record the jti")
}
