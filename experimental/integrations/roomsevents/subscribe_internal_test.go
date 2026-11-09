package roomsevents

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/panyam/mcpkit/experimental/ext/events"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/ext/rooms/memroom"
)

// hookCtx is a HookContext with no MCP session in it, which no test client
// can produce against a bridge.
type hookCtx struct{}

func (hookCtx) Context() context.Context  { return context.Background() }
func (hookCtx) Principal() string         { return "alice" }
func (hookCtx) SubscriptionID() string    { return "sub-1" }
func (hookCtx) Mode() events.DeliveryMode { return events.DeliveryModePush }

func TestOnSubscribe_RefusesACallWithoutASession(t *testing.T) {
	e := New()
	b := rooms.New(memroom.New().Transport("bridge"))
	t.Cleanup(b.Close)
	e.Bind(b)

	err := e.onSubscribe(hookCtx{}, map[string]any{"resource": "design:demo"})
	require.Error(t, err)
	assert.ErrorIs(t, err, events.ErrSubscribeForbidden)
	assert.Contains(t, err.Error(), "need an MCP session")
}
