package server

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// sessionKeepalive sends periodic JSON-RPC ping requests to a client via
// its GET SSE stream. If maxFailures consecutive pings fail (timeout or
// error), onDeath is called to clean up the session.
//
// The ping uses the existing server-to-client request infrastructure
// (sendServerRequest), so the client must POST back a response.
type sessionKeepalive struct {
	interval    time.Duration
	maxFailures int
	requestFunc func(ctx context.Context, method string, params any) (json.RawMessage, error)
	onDeath     func()
	cancel      context.CancelFunc
}

// start begins the keepalive goroutine. Call stop to terminate it.
func (k *sessionKeepalive) start() {
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	go k.run(ctx)
}

// stop terminates the keepalive goroutine.
func (k *sessionKeepalive) stop() {
	if k.cancel != nil {
		k.cancel()
	}
}

func (k *sessionKeepalive) run(ctx context.Context) {
	ticker := time.NewTicker(k.interval)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Send ping with a timeout of half the interval
			pingCtx, cancel := context.WithTimeout(ctx, k.interval/2)
			_, err := k.requestFunc(pingCtx, "ping", nil)
			cancel()

			if err != nil {
				failures++
				log.Printf("mcpkit: keepalive ping failed (%d/%d): %v", failures, k.maxFailures, err)
				if failures >= k.maxFailures {
					log.Printf("mcpkit: keepalive max failures reached, closing session")
					k.onDeath()
					return
				}
			} else {
				failures = 0
			}
		}
	}
}
