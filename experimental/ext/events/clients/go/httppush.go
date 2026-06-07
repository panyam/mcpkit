package eventsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Pusher is the client-side counterpart to events.HTTPSource. Use it from
// a source-manager process (e.g., the push-server tier in
// examples/events/whole-enchilada) to push typed events into a remote
// mcpkit Events server over HTTP.
//
// The wire is intentionally simple: a JSON-encoded payload POSTed to
// {BaseURL}/events/{eventName}/inject with an optional Bearer header.
// The event-server's HTTPSource Handler decodes and yields it into the
// library-managed YieldingSource that fans out to push / poll / webhook
// subscribers.
//
// Pusher is safe for concurrent use; the embedded *http.Client is the
// only shared state. Construct via NewPusher; use PushNamed for typed
// payloads, or Push if you already have a JSON-encoded byte slice.
type Pusher struct {
	baseURL string
	bearer  string
	client  *http.Client
}

// PusherOption configures optional Pusher behavior.
type PusherOption func(*Pusher)

// WithPusherHTTPClient overrides the default http.Client (10s timeout).
// Use to set custom timeouts, transports, or instrumentation.
func WithPusherHTTPClient(c *http.Client) PusherOption {
	return func(p *Pusher) { p.client = c }
}

// NewPusher constructs a Pusher targeting an event-server at baseURL.
// baseURL is the scheme+host[:port] of the event-server (e.g.
// "http://event-server:8080"); the per-event path is appended by Push.
// bearer is the shared secret configured on the event-server's
// HTTPSource via events.HTTPSourceConfig.Bearer; empty means the
// event-server's inject endpoint is open.
//
// Default http.Client has a 10-second per-request timeout. Override
// via WithPusherHTTPClient.
func NewPusher(baseURL string, bearer string, opts ...PusherOption) *Pusher {
	p := &Pusher{
		baseURL: strings.TrimRight(baseURL, "/"),
		bearer:  bearer,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// PushNamed JSON-encodes data and POSTs it to
// {BaseURL}/events/{eventName}/inject. Returns nil when the server
// responds 202 Accepted; otherwise returns a *PushError carrying the
// status code and body. The context controls cancellation; respect it
// in callers that may want to abandon a push under shutdown pressure.
func (p *Pusher) PushNamed(ctx context.Context, eventName string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode event payload: %w", err)
	}
	return p.pushRaw(ctx, eventName, body)
}

// Push POSTs an already-JSON-encoded body. Use when the caller has a
// pre-marshaled payload it wants to avoid re-encoding (e.g., bridging
// from another wire). Otherwise prefer PushNamed.
func (p *Pusher) Push(ctx context.Context, eventName string, body []byte) error {
	return p.pushRaw(ctx, eventName, body)
}

func (p *Pusher) pushRaw(ctx context.Context, eventName string, body []byte) error {
	url := p.baseURL + "/events/" + eventName + "/inject"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build inject request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("post inject: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &PushError{
			EventName:  eventName,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	return nil
}

// PushError is returned by Pusher when the event-server rejects an
// inject with a non-202 status. The Body field is truncated to 4 KiB to
// keep error messages bounded.
type PushError struct {
	EventName  string
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *PushError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("push %q: status %d", e.EventName, e.StatusCode)
	}
	return fmt.Sprintf("push %q: status %d: %s", e.EventName, e.StatusCode, e.Body)
}
