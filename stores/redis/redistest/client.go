// Package redistest provides a miniredis-backed *redis.Client
// constructor for tests in any module that depends on the root
// stores/redis adapter (mcpkit/stores/redis) or the events SDK's
// per-event-name redisstore Bus
// (mcpkit/experimental/ext/events/stores/redis).
//
// Unlike the legacy package-private newTestClient helper in
// stores/redis_test.go, this package is non-test-suffixed so it can
// be imported across module boundaries. Files here are NOT shipped to
// production callers — but the import path being non-test means
// adopters could pull this in. The constructor's NewClient name
// (rather than a package-internal lowercase one) makes that intent
// explicit while keeping adopters' tests terse.
//
// Adopters should NOT import this package outside *_test.go files —
// it's test infrastructure. There's no compile-time enforcement of
// that constraint; treat it as convention.
package redistest

import (
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// NewClient returns a *redis.Client and registers cleanup so callers
// don't have to defer Close. By default it spins up a fresh miniredis
// (in-process pure-Go Redis); when MCPKIT_EVENTS_TEST_REDIS_ADDR is
// set, it connects to that address instead so a real Redis can run
// the same assertions.
//
// Each call returns a logically-isolated Redis (separate miniredis
// instance, or the same real Redis flushed beforehand) so concurrent
// tests don't see each other's traffic.
func NewClient(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("MCPKIT_EVENTS_TEST_REDIS_ADDR")
	if addr == "" {
		mr, err := miniredis.Run()
		require.NoError(t, err)
		t.Cleanup(mr.Close)
		addr = mr.Addr()
	}
	cli := redis.NewClient(&redis.Options{Addr: addr})
	require.NoError(t, cli.FlushDB(t.Context()).Err())
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}
