package rooms_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachedSince_KeepsTheFirstAttachAndForgetsOnDetach(t *testing.T) {
	st := newStack(t)
	a := st.agent(t, "alice", allScopes...)
	_, ok := st.bridge.AttachedSince(a.SessionID(), room)
	assert.False(t, ok, "not attached yet")

	before := time.Now()
	a.attach(t, room)
	first, ok := st.bridge.AttachedSince(a.SessionID(), room)
	require.True(t, ok)
	assert.False(t, first.Before(before.Add(-time.Second)), "attached at %v, before the call at %v", first, before)

	time.Sleep(20 * time.Millisecond)
	a.attach(t, room)
	again, ok := st.bridge.AttachedSince(a.SessionID(), room)
	require.True(t, ok)
	assert.True(t, again.Equal(first), "a repeated attach keeps the first time")

	ok2, out := call(t, a, "detach", map[string]any{"resource": room})
	require.True(t, ok2, out)
	_, ok = st.bridge.AttachedSince(a.SessionID(), room)
	assert.False(t, ok, "detach forgets it")

	time.Sleep(20 * time.Millisecond)
	a.attach(t, room)
	later, ok := st.bridge.AttachedSince(a.SessionID(), room)
	require.True(t, ok)
	assert.True(t, later.After(first), "a new attach starts a new time")
}
