package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// DefaultFailoverCooldown is how long after a primary failure the failover
// provider keeps routing to the backup before re-trying the primary.
const DefaultFailoverCooldown = 30 * time.Second

// FailoverConfig assembles a FailoverProvider.
type FailoverConfig struct {
	// Primary is the preferred provider. Required.
	Primary Provider

	// Backup takes over when the primary fails cleanly. Required (a
	// failover wrapper with no backup is just the primary).
	Backup Provider

	// Cooldown is how long the primary stays benched after a failure
	// before the next call re-tries it (lazy recovery). Zero means
	// DefaultFailoverCooldown.
	Cooldown time.Duration

	// Logger receives failover transitions at Warn and recoveries at
	// Info, with structured attrs. Nil discards: the agent module never
	// writes to process-global outputs (constraint A4).
	Logger *slog.Logger

	// now is injectable for tests; nil means time.Now.
	now func() time.Time
}

// ProviderHealth is the pollable failover snapshot, the host-facing
// equivalent of a connection-status endpoint.
type ProviderHealth struct {
	// Active is "primary" or "backup".
	Active string `json:"active"`

	// ConsecutiveFailures counts primary failures since its last success.
	ConsecutiveFailures int `json:"consecutiveFailures"`

	// LastError is the most recent primary failure, empty when healthy.
	LastError string `json:"lastError,omitempty"`

	// LastFailureAt is when the primary last failed (zero when never).
	LastFailureAt time.Time `json:"lastFailureAt,omitzero"`
}

// FailoverProvider fronts a primary and a backup Provider behind the plain
// Provider interface, so the Runner never knows failover exists. Semantics:
//
//   - A call that fails CLEANLY on the primary (the call itself errors
//     before any delta was delivered) is transparently retried on the
//     backup, once. A stream that already emitted deltas is never retried:
//     the consumer observed partial output, and replaying could re-run
//     side effects downstream.
//   - After a primary failure, calls route to the backup until Cooldown
//     elapses; the next call after that re-tries the primary (lazy
//     recovery). StartReconciler adds an optional background probe.
//
// Safe for concurrent use.
type FailoverProvider struct {
	cfg FailoverConfig

	mu           sync.Mutex
	failures     int
	lastErr      error
	lastFailure  time.Time
	benchedUntil time.Time
}

// NewFailoverProvider validates cfg and returns the wrapper.
func NewFailoverProvider(cfg FailoverConfig) (*FailoverProvider, error) {
	if cfg.Primary == nil || cfg.Backup == nil {
		return nil, errors.New("agent: FailoverConfig requires Primary and Backup")
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = DefaultFailoverCooldown
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return &FailoverProvider{cfg: cfg}, nil
}

// Health returns the current snapshot.
func (f *FailoverProvider) Health() ProviderHealth {
	f.mu.Lock()
	defer f.mu.Unlock()
	h := ProviderHealth{Active: "primary", ConsecutiveFailures: f.failures, LastFailureAt: f.lastFailure}
	if f.cfg.now().Before(f.benchedUntil) {
		h.Active = "backup"
	}
	if f.lastErr != nil {
		h.LastError = f.lastErr.Error()
	}
	return h
}

func (f *FailoverProvider) primaryBenched() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg.now().Before(f.benchedUntil)
}

func (f *FailoverProvider) recordFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures++
	f.lastErr = err
	f.lastFailure = f.cfg.now()
	f.benchedUntil = f.lastFailure.Add(f.cfg.Cooldown)
	f.cfg.Logger.Warn("provider failover: primary failed, routing to backup",
		"error", err.Error(),
		"consecutive_failures", f.failures,
		"cooldown", f.cfg.Cooldown.String())
}

func (f *FailoverProvider) recordRecovery() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.cfg.Logger.Info("provider failover: primary recovered", "after_failures", f.failures)
	}
	f.failures = 0
	f.lastErr = nil
	f.benchedUntil = time.Time{}
}

// Stream implements Provider. See the type doc for retry semantics; the
// no-retry-after-deltas rule is enforced by wrapping the primary stream so a
// mid-stream failure surfaces to the caller instead of restarting.
func (f *FailoverProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	if f.primaryBenched() {
		return f.cfg.Backup.Stream(ctx, req)
	}
	s, err := f.cfg.Primary.Stream(ctx, req)
	if err != nil {
		f.recordFailure(err)
		return f.cfg.Backup.Stream(ctx, req)
	}
	return &failoverStream{f: f, inner: s}, nil
}

// Generate implements Provider with the same clean-failure retry.
func (f *FailoverProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	if f.primaryBenched() {
		return f.cfg.Backup.Generate(ctx, req)
	}
	resp, err := f.cfg.Primary.Generate(ctx, req)
	if err != nil {
		f.recordFailure(err)
		return f.cfg.Backup.Generate(ctx, req)
	}
	f.recordRecovery()
	return resp, nil
}

// StartReconciler probes the primary every interval while it is benched, so
// recovery does not wait for user traffic. probe runs a caller-supplied
// cheap check (nil uses a one-token Generate against the primary). Returns
// a stop func; also stops when ctx ends.
func (f *FailoverProvider) StartReconciler(ctx context.Context, interval time.Duration, probe func(context.Context) error) (stop func()) {
	if probe == nil {
		probe = func(ctx context.Context) error {
			_, err := f.cfg.Primary.Generate(ctx, ProviderRequest{
				Messages:  []Message{{Role: RoleUser, Text: "ping"}},
				MaxTokens: 1,
			})
			return err
		}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-t.C:
				if !f.primaryBenched() {
					continue
				}
				if probe(ctx) == nil {
					f.recordRecovery()
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// failoverStream marks the primary healthy once its stream completes and
// records (without retrying) a failure that happens after deltas flowed.
type failoverStream struct {
	f         *FailoverProvider
	inner     Stream
	delivered bool
}

// Recv implements Stream.
func (s *failoverStream) Recv() (Delta, error) {
	d, err := s.inner.Recv()
	switch {
	case err == nil:
		s.delivered = true
		return d, nil
	case errors.Is(err, io.EOF):
		s.f.recordRecovery()
		return d, err
	default:
		// Bench the primary for future calls either way, but this
		// stream's consumer must see the error: after partial output a
		// silent replay could re-run downstream side effects, and even
		// before output the caller already holds this stream.
		s.f.recordFailure(err)
		return d, err
	}
}

// Close implements Stream.
func (s *failoverStream) Close() error { return s.inner.Close() }
