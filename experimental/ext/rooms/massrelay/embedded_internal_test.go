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
