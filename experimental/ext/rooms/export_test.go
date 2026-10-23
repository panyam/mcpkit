package rooms

// SessionCount reports how many MCP sessions the bridge holds state for.
func (b *Bridge) SessionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

// NextRequestWaiting reports how many next_request calls are holding.
func (b *Bridge) NextRequestWaiting() int { return int(b.fb.waiting.Load()) }
