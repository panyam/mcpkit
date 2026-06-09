package main

import (
	"context"
	"log"
	"math/rand"
	"time"
)

// runChatFeeder yields a synthetic chat message every interval until
// ctx is done, cycling across a small fixed set of channels so the
// Match-by-channel demo has events on every channel to filter.
//
// yield takes ctx so SEP-414 trace context (when configured) flows
// from the feeder's ctx into event.Meta.traceparent — same shape PR
// 712 established for discord / telegram / whole-enchilada feeders.
func runChatFeeder(ctx context.Context, yield func(context.Context, ChatMessageData) error, interval time.Duration) {
	channels := []string{"general", "dev", "alerts"}
	senders := []string{"alice", "bob", "carol", "dave"}
	templates := []string{
		"shipping new build",
		"any reviews left for #1234?",
		"meeting at 3pm",
		"deploy looks healthy",
		"+1",
		"PSA: staging is down",
	}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ts := <-t.C:
			msg := ChatMessageData{
				Channel:   channels[rng.Intn(len(channels))],
				Sender:    senders[rng.Intn(len(senders))],
				Text:      templates[rng.Intn(len(templates))],
				Timestamp: ts.UTC().Format(time.RFC3339),
			}
			if err := yield(ctx, msg); err != nil {
				log.Printf("[chat] yield: %v", err)
			}
		}
	}
}

// injectChat is a /inject-style helper exposed to the walkthrough so
// the live demo can deterministically post one event of a given
// channel rather than wait for the random feeder to land it.
func injectChat(ctx context.Context, yield func(context.Context, ChatMessageData) error, channel, sender, text string) error {
	return yield(ctx, ChatMessageData{
		Channel:   channel,
		Sender:    sender,
		Text:      text,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
}
