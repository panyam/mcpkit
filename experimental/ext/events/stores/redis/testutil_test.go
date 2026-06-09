package redisstore

import (
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// newTestClient returns a *redis.Client and a cleanup func. By default
// it spins up a fresh miniredis (in-process pure-Go Redis); when
// MCPKIT_EVENTS_TEST_REDIS_ADDR is set, it connects to that address
// instead so `make testredis` can run the same assertions against a
// real Redis. The cleanup func is registered with t.Cleanup so the
// caller doesn't have to defer.
//
// Each call returns a logically-isolated Redis (separate miniredis
// instance, or the same real Redis flushed beforehand) so concurrent
// tests don't see each other's traffic.
func newTestClient(t *testing.T) *redis.Client {
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
