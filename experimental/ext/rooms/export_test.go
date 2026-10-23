package rooms

// SessionCount reports how many MCP sessions the bridge holds state for.
func (b *Bridge) SessionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}
