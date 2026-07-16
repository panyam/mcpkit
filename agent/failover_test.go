package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type flakyProvider struct {
	*StubProvider
	failCalls int
	calls     int
	failErr   error
}

func (p *flakyProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	p.calls++
	if p.calls <= p.failCalls {
		return nil, p.failErr
	}
	return p.StubProvider.Stream(ctx, req)
}

func (p *flakyProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	p.calls++
	if p.calls <= p.failCalls {
		return nil, p.failErr
	}
	return p.StubProvider.Generate(ctx, req)
}

func newFailover(t *testing.T, cfg FailoverConfig) (*FailoverProvider, *bytes.Buffer, *time.Time) {
	t.Helper()
	var logBuf bytes.Buffer
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cfg.Logger = slog.New(slog.NewTextHandler(&logBuf, nil))
	cfg.now = func() time.Time { return now }
	f, err := NewFailoverProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return f, &logBuf, &now
}

func TestFailoverCleanFailureRetriesBackupOnce(t *testing.T) {
	boom := errors.New("connection refused")
	primary := &flakyProvider{StubProvider: NewStubProvider(), failCalls: 99, failErr: boom}
	backup := NewStubProvider(StubTurn{Text: "from backup"})

	f, logBuf, _ := newFailover(t, FailoverConfig{Primary: primary, Backup: backup})
	s, err := f.Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var acc Accumulator
	for {
		d, err := s.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(d)
	}
	if acc.Result().Text != "from backup" {
		t.Fatalf("backup must serve the turn, got %+v", acc.Result())
	}
	h := f.Health()
	if h.Active != "backup" || h.ConsecutiveFailures != 1 || !strings.Contains(h.LastError, "connection refused") {
		t.Fatalf("health = %+v", h)
	}
	if !strings.Contains(logBuf.String(), "routing to backup") {
		t.Fatalf("transition must be logged:\n%s", logBuf.String())
	}
}

func TestFailoverBenchesUntilCooldownThenRecovers(t *testing.T) {
	boom := errors.New("dial fail")
	primary := &flakyProvider{StubProvider: NewStubProvider(StubTurn{Text: "primary back"}), failCalls: 1, failErr: boom}
	backup := NewStubProvider(StubTurn{Text: "b1"}, StubTurn{Text: "b2"})

	f, logBuf, now := newFailover(t, FailoverConfig{Primary: primary, Backup: backup, Cooldown: time.Minute})

	if resp, _ := f.Generate(context.Background(), ProviderRequest{}); resp.Text != "b1" {
		t.Fatalf("first call must fail over, got %+v", resp)
	}
	if resp, _ := f.Generate(context.Background(), ProviderRequest{}); resp.Text != "b2" {
		t.Fatalf("within cooldown must route to backup without touching primary, got %+v", resp)
	}
	if primary.calls != 1 {
		t.Fatalf("benched primary must not be called, calls = %d", primary.calls)
	}

	*now = now.Add(2 * time.Minute)
	resp, err := f.Generate(context.Background(), ProviderRequest{})
	if err != nil || resp.Text != "primary back" {
		t.Fatalf("post-cooldown call must re-try primary: %+v %v", resp, err)
	}
	if h := f.Health(); h.Active != "primary" || h.ConsecutiveFailures != 0 || h.LastError != "" {
		t.Fatalf("recovered health = %+v", h)
	}
	if !strings.Contains(logBuf.String(), "primary recovered") {
		t.Fatalf("recovery must be logged:\n%s", logBuf.String())
	}
}

type midStreamFailProvider struct{}

func (midStreamFailProvider) Stream(ctx context.Context, req ProviderRequest) (Stream, error) {
	return &midFailStream{}, nil
}
func (midStreamFailProvider) Generate(ctx context.Context, req ProviderRequest) (*ProviderResponse, error) {
	return nil, errors.New("unused")
}

type midFailStream struct{ n int }

func (s *midFailStream) Recv() (Delta, error) {
	s.n++
	if s.n == 1 {
		return Delta{Kind: DeltaText, Text: "partial"}, nil
	}
	return Delta{}, errors.New("connection reset mid-stream")
}
func (s *midFailStream) Close() error { return nil }

func TestFailoverNeverRetriesAfterDeltas(t *testing.T) {
	backup := NewStubProvider(StubTurn{Text: "must not be used for this stream"})
	f, _, _ := newFailover(t, FailoverConfig{Primary: midStreamFailProvider{}, Backup: backup})

	s, err := f.Stream(context.Background(), ProviderRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Recv(); err != nil || d.Text != "partial" {
		t.Fatalf("first delta: %+v %v", d, err)
	}
	if _, err := s.Recv(); err == nil || !strings.Contains(err.Error(), "mid-stream") {
		t.Fatalf("mid-stream failure must surface to the consumer, got %v", err)
	}
	if len(backup.Requests()) != 0 {
		t.Fatal("backup must not be invoked for a stream that already delivered")
	}
	if h := f.Health(); h.Active != "backup" {
		t.Fatalf("primary must still be benched for FUTURE calls: %+v", h)
	}
}

func TestFailoverReconcilerRecoversWithoutTraffic(t *testing.T) {
	boom := errors.New("down")
	primary := &flakyProvider{StubProvider: NewStubProvider(), failCalls: 1, failErr: boom}
	backup := NewStubProvider(StubTurn{Text: "b"})
	f, logBuf, _ := newFailover(t, FailoverConfig{Primary: primary, Backup: backup, Cooldown: time.Hour})

	f.Generate(context.Background(), ProviderRequest{})
	if f.Health().Active != "backup" {
		t.Fatal("setup: primary must be benched")
	}

	probed := make(chan struct{}, 1)
	stop := f.StartReconciler(context.Background(), 10*time.Millisecond, func(ctx context.Context) error {
		select {
		case probed <- struct{}{}:
		default:
		}
		return nil
	})
	defer stop()

	select {
	case <-probed:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler never probed")
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.Health().Active != "primary" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.Health().Active != "primary" {
		t.Fatalf("reconciler must un-bench the primary: %+v", f.Health())
	}
	if !strings.Contains(logBuf.String(), "primary recovered") {
		t.Fatalf("recovery log missing:\n%s", logBuf.String())
	}
}

func TestFailoverRequiresBothProviders(t *testing.T) {
	if _, err := NewFailoverProvider(FailoverConfig{Primary: NewStubProvider()}); err == nil {
		t.Fatal("want error without backup")
	}
}
