package massrelay

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

func TestMissingPeers_ReadsTheRelaysMessage(t *testing.T) {
	assert.Equal(t, []rooms.PeerID{"a", "b"}, missingPeers("Not in this room: a, b"))
	assert.Equal(t, []rooms.PeerID{"a"}, missingPeers("Not in this room: a"))
	assert.Empty(t, missingPeers("something else"))
}

func TestDroppedAction_ReadsTheRelaysMessage(t *testing.T) {
	id, ok := droppedAction(`Dropped action "d12": over 30 messages/sec`)
	assert.True(t, ok)
	assert.Equal(t, "d12", id)
	id, ok = droppedAction(`Dropped action "a \"quoted\" id": over 30 messages/sec`)
	assert.True(t, ok)
	assert.Equal(t, `a "quoted" id`, id)
	_, ok = droppedAction("Not in this room: a, b")
	assert.False(t, ok)
}
