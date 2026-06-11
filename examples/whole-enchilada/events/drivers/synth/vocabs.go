package main

import (
	"math/rand"
	"sort"
	"time"
)

// vocab is the event-shape-specific knowledge a driver needs: how to
// build one random JSON payload + a default cadence. The HTTP path,
// retry loop, signal handling, tenant rotation, and seq counter are
// vocab-agnostic and live in main.go. sample receives the seq so
// receivers can detect dropped posts (seq-with-gaps) and order
// (monotone-increasing per driver run).
type vocab struct {
	defaultEvery time.Duration
	rngOffset    int64
	sample       func(rng *rand.Rand, ts, tenant string, seq int) any
}

// vocabs maps event source name → vocab. To add a new event type:
// define its random sampler + payload struct here, register in the
// map, done. No new binary, no new Makefile target shape needed.
var vocabs = map[string]vocab{
	"chat.message":     {defaultEvery: 2 * time.Second, rngOffset: 0, sample: sampleChat},
	"presence.changed": {defaultEvery: 5 * time.Second, rngOffset: 1, sample: samplePresence},
}

func vocabNames() []string {
	out := make([]string, 0, len(vocabs))
	for k := range vocabs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// chatMessage mirrors the event-server's ChatMessageData wire shape
// plus a driver-side seq counter — extra fields pass through the
// event-server's HTTPSource untouched and surface in subscriber logs.
type chatMessage struct {
	Tenant    string `json:"tenant,omitempty"`
	Channel   string `json:"channel"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Timestamp string `json:"ts"`
	Seq       int    `json:"seq"`
}

func sampleChat(rng *rand.Rand, ts, tenant string, seq int) any {
	senders := []string{"alice", "bob", "carol", "dave"}
	channels := []string{"general", "random", "alerts"}
	templates := []string{
		"shipping new build",
		"anyone tried the new dashboard?",
		"meeting at 3pm",
		"+1",
		"merged",
		"reviewing now",
		"PSA: the staging env is down",
	}
	return chatMessage{
		Tenant:    tenant,
		Channel:   channels[rng.Intn(len(channels))],
		Sender:    senders[rng.Intn(len(senders))],
		Text:      templates[rng.Intn(len(templates))],
		Timestamp: ts,
		Seq:       seq,
	}
}

// presenceChange mirrors the event-server's PresenceChangedData wire shape
// plus a driver-side seq counter — same passthrough semantics as chatMessage.
type presenceChange struct {
	Tenant    string `json:"tenant,omitempty"`
	User      string `json:"user"`
	State     string `json:"state"`
	Timestamp string `json:"ts"`
	Seq       int    `json:"seq"`
}

func samplePresence(rng *rand.Rand, ts, tenant string, seq int) any {
	users := []string{"alice", "bob", "carol", "dave"}
	states := []string{"online", "away", "offline"}
	return presenceChange{
		Tenant:    tenant,
		User:      users[rng.Intn(len(users))],
		State:     states[rng.Intn(len(states))],
		Timestamp: ts,
		Seq:       seq,
	}
}
