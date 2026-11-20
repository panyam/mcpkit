package eventsclient

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
	events "github.com/panyam/mcpkit/experimental/ext/events"
)

// minCardRefetch spaces out card refetches triggered by an unknown key, so a
// stream of deliveries under a kid the card doesn't list costs one fetch per
// interval rather than one per delivery. A var so tests can shorten it.
var minCardRefetch = 10 * time.Second

// WithServerIdentity makes the receiver accept only deliveries that also
// carry a v1a signature from a key the server publishes on its Server Card
// (SEP-3415 §"Webhook security", server identity). The card is fetched from
// mcpURL's own origin with client.ServerCardCache, so a redirect to another
// origin, or a plain-http URL without client.WithInsecureCardFetch in opts,
// leaves the receiver with no keys and every delivery is refused with 401.
//
// The keys come from the card remote whose url is mcpURL. A delivery that no
// cached key verifies triggers one refetch, at most every 10 seconds, which
// picks up a key the server added during rotation. The HMAC check runs
// first, so only deliveries signed with the subscription secret can trigger a
// fetch.
func WithServerIdentity(mcpURL string, opts ...client.CardFetchOption) ReceiverOption {
	id := &serverIdentity{mcpURL: mcpURL, cache: client.NewServerCardCache(opts...)}
	return func(c *receiverConfig) { c.identity = id }
}

type serverIdentity struct {
	mcpURL string
	cache  *client.ServerCardCache

	mu        sync.Mutex
	keys      events.CardWebhooksInfo
	fetchedAt time.Time
}

func (s *serverIdentity) verify(ctx context.Context, h http.Header, body []byte) bool {
	check := func(info events.CardWebhooksInfo) bool {
		if info.SigningKeys == nil {
			return false
		}
		_, ok := events.VerifyServerIdentity(*info.SigningKeys, h.Get("webhook-id"), h.Get("webhook-timestamp"), body, h.Get("webhook-signature"))
		return ok
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fetchedAt.IsZero() && check(s.keys) {
		return true
	}
	if !s.fetchedAt.IsZero() && time.Since(s.fetchedAt) < minCardRefetch {
		return false
	}
	s.refresh(ctx)
	return check(s.keys)
}

// refresh fetches the card and keeps its events keys. A failed fetch leaves
// no keys, so the receiver fails closed. Callers hold s.mu.
func (s *serverIdentity) refresh(ctx context.Context) {
	s.fetchedAt = time.Now()
	s.keys = events.CardWebhooksInfo{}
	card, err := s.cache.Fetch(ctx, s.mcpURL)
	if err != nil {
		return
	}
	remote, ok := client.CardRemote(card, s.mcpURL)
	if !ok {
		return
	}
	if info, ok := events.CardWebhooks(remote.ExtensionsInfo); ok {
		s.keys = info
	}
}
