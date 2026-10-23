package bridge

import (
	"testing"
	"time"

	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingApprover_ListsOldestFirst(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	clock := []time.Time{base.Add(2 * time.Second), base, base.Add(time.Second)}
	next := make(chan time.Time, len(clock))
	for _, c := range clock {
		next <- c
	}
	p := &PendingApprover{Timeout: time.Minute, now: func() time.Time { return <-next }}
	for n := range len(clock) {
		go func() { _, _ = p.Approve(t.Context(), eventsclient.Delivery{}, nil) }()
		require.Eventually(t, func() bool { return len(p.Pending()) == n+1 }, time.Second, time.Millisecond)
	}
	got := p.Pending()
	require.Len(t, got, 3)
	assert.True(t, got[0].RequestedAt.Equal(base))
	assert.True(t, got[1].RequestedAt.Equal(base.Add(time.Second)))
	assert.True(t, got[2].RequestedAt.Equal(base.Add(2*time.Second)))
	for _, a := range got {
		require.NoError(t, p.Decide(a.ID, false))
	}
}
