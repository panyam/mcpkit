package eventsclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// SubscribeOptions configures a Subscription at startup.
type SubscribeOptions struct {
	// EventName is the source name on the server (e.g., "discord.message").
	EventName string

	// CallbackURL is the publicly-reachable URL the server will POST to.
	CallbackURL string

	// Secret is the client-supplied HMAC signing secret. Per spec, must
	// be whsec_ + base64 of 24-64 random bytes. If empty, the SDK
	// auto-generates a spec-conformant value via events.GenerateSecret().
	// Subscription.Secret() returns the value the SDK ended up using
	// (supplied or generated) — the receiver verifies signatures
	// against this same value.
	Secret string

	// Cursor controls the resume point. nil = "from now" (server returns
	// its current head). Non-nil = explicit resume cursor.
	Cursor *string

	// RefreshFactor schedules the auto-refresh at RefreshFactor * TTL.
	// Defaults to 0.5. Lower values refresh more aggressively (more spec-
	// compliant but more network traffic); higher values conserve calls
	// but risk crossing the TTL boundary.
	RefreshFactor float64

	// OnRefresh fires after each successful subscribe call (the initial
	// one and every refresh, including post-recovery resubscribes).
	OnRefresh func()

	// OnRecover fires when a refresh failed (the registry had already
	// expired the subscription) and a fresh subscribe succeeded.
	OnRecover func()
}

// Subscription manages an events/subscribe lifecycle with automatic TTL
// refresh. Construct via Subscribe; stop via Stop or by cancelling the
// parent context.
type Subscription struct {
	mu            sync.RWMutex
	sess          *client.Client
	opts          SubscribeOptions
	id            string
	secret        string
	cursor        *string
	refreshBefore time.Time
	stop          chan struct{}
	stopped       chan struct{}
}

// Subscribe sends events/subscribe and starts the background refresh loop.
// Returns once the initial subscribe has succeeded, so callers can read
// Secret / ID / RefreshBefore synchronously after.
func Subscribe(parent context.Context, sess *client.Client, opts SubscribeOptions) (*Subscription, error) {
	if opts.RefreshFactor <= 0 {
		opts.RefreshFactor = 0.5
	}
	if opts.RefreshFactor >= 1 {
		return nil, fmt.Errorf("eventsclient: RefreshFactor must be < 1 (got %g) — refreshing at TTL or later races the boundary", opts.RefreshFactor)
	}
	// Spec mandates client-supplied delivery.secret (whsec_ + base64 of
	// 24-64 random bytes). Auto-generate on the application's behalf
	// when the caller didn't supply one — Standard Webhooks profile
	// recommends generating from a CSPRNG by default.
	if opts.Secret == "" {
		opts.Secret = events.GenerateSecret()
	}

	s := &Subscription{
		sess:    sess,
		opts:    opts,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}

	if err := s.subscribe(); err != nil {
		return nil, fmt.Errorf("eventsclient: initial subscribe failed: %w", err)
	}
	if opts.OnRefresh != nil {
		opts.OnRefresh()
	}

	go s.refreshLoop(parent)
	return s, nil
}

// ID returns the subscription id the SDK supplied (echoed by the
// server). γ replaces id-keyed subscription identity with the
// (principal, name, params, url) tuple per spec.
func (s *Subscription) ID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

// Secret returns the secret the SDK is using to sign deliveries —
// either the value the caller passed in SubscribeOptions.Secret, or
// the auto-generated whsec_ value the SDK produced when Secret was
// empty. The receiver verifies signatures with this same value.
func (s *Subscription) Secret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.secret
}

// Cursor returns the resume cursor the server stamped onto the most recent
// subscribe response. Nil for cursorless sources.
func (s *Subscription) Cursor() *string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.cursor == nil {
		return nil
	}
	c := *s.cursor
	return &c
}

// RefreshBefore returns the timestamp by which the next refresh must
// land. Refreshes are scheduled at RefreshFactor * (RefreshBefore - now).
func (s *Subscription) RefreshBefore() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.refreshBefore
}

// Stop ends the refresh loop. Does not unsubscribe — call sess.Call
// "events/unsubscribe" yourself if you want server-side cleanup before
// the soft-state TTL evicts the entry naturally.
func (s *Subscription) Stop() {
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
	<-s.stopped
}

// subscribe performs one events/subscribe round-trip and updates state.
// Caller is responsible for firing OnRefresh / OnRecover; this method
// only handles the network call and state update.
//
// Per spec §"Subscription Identity" → "Derived id" L367, the server
// derives the subscription id from (principal, name, params, url) — the
// SDK does not send an id; it reads the derived id back from the
// response (Subscription.ID()).
func (s *Subscription) subscribe() error {
	params := map[string]any{
		"name": s.opts.EventName,
		"delivery": map[string]any{
			"mode":   "webhook",
			"url":    s.opts.CallbackURL,
			"secret": s.opts.Secret,
		},
	}
	if s.opts.Cursor != nil {
		params["cursor"] = *s.opts.Cursor
	} else {
		// Explicit JSON null so the server resolves to "from now".
		params["cursor"] = nil
	}

	resp, err := s.sess.Call("events/subscribe", params)
	if err != nil {
		return err
	}

	// Per spec the response no longer carries `secret` — the SDK
	// already knows the value it supplied (s.opts.Secret).
	var result struct {
		ID            string  `json:"id"`
		Cursor        *string `json:"cursor"`
		RefreshBefore string  `json:"refreshBefore"`
	}
	if err := unmarshalResult(resp.Raw, &result); err != nil {
		return fmt.Errorf("decode subscribe response: %w", err)
	}

	rb, err := time.Parse(time.RFC3339, result.RefreshBefore)
	if err != nil {
		return fmt.Errorf("parse refreshBefore %q: %w", result.RefreshBefore, err)
	}

	s.mu.Lock()
	s.id = result.ID
	s.secret = s.opts.Secret
	s.cursor = result.Cursor
	s.refreshBefore = rb
	s.mu.Unlock()
	return nil
}

// refreshLoop runs in the background until Stop or context cancel. On a
// failed refresh (subscription expired in the registry) it immediately
// resubscribes and fires OnRecover.
func (s *Subscription) refreshLoop(parent context.Context) {
	defer close(s.stopped)
	for {
		wait := s.nextRefreshDelay()
		timer := time.NewTimer(wait)
		select {
		case <-parent.Done():
			timer.Stop()
			return
		case <-s.stop:
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := s.subscribe(); err != nil {
			// Treat any error as "subscription likely expired" and try a
			// fresh subscribe. Per upstream WG line 623 (comment
			// r3140476053), the boundary race is expected and the
			// recovery action is the same call.
			if errors.Is(err, context.Canceled) {
				return
			}
			if recoverErr := s.subscribe(); recoverErr != nil {
				// Bubble up via the next iteration's wait. No log here —
				// we don't want the SDK to spam stderr in user processes.
				continue
			}
			if s.opts.OnRefresh != nil {
				s.opts.OnRefresh()
			}
			if s.opts.OnRecover != nil {
				s.opts.OnRecover()
			}
			continue
		}
		if s.opts.OnRefresh != nil {
			s.opts.OnRefresh()
		}
	}
}

// nextRefreshDelay returns how long to wait before the next refresh.
// Bounded by [1s, RefreshFactor * (RefreshBefore - now)].
func (s *Subscription) nextRefreshDelay() time.Duration {
	s.mu.RLock()
	rb := s.refreshBefore
	s.mu.RUnlock()
	remaining := time.Until(rb)
	if remaining <= 0 {
		return 1 * time.Second
	}
	wait := time.Duration(float64(remaining) * s.opts.RefreshFactor)
	if wait < 1*time.Second {
		wait = 1 * time.Second
	}
	return wait
}

