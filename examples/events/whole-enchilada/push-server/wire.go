// Package main implements the push-server tier of the whole-enchilada
// demo. It owns the source-side concerns (synthetic upstream feeders
// in this stage; in production stages: Discord bot lifecycle, OAuth
// refresh, polling external APIs) and pushes typed events to the
// event-server tier over HTTP using events.HTTPSource.
package main

// ChatMessageData mirrors the event-server's ChatMessageData wire shape.
// In a real deployment this type would live in a shared package or be
// codegen'd from a schema; copy-paste is fine for the stage-1 demo.
type ChatMessageData struct {
	Channel   string `json:"channel"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Timestamp string `json:"ts"`
}

// PresenceChangedData mirrors the event-server's PresenceChangedData
// wire shape. Cursorless on the event-server side.
type PresenceChangedData struct {
	User      string `json:"user"`
	State     string `json:"state"`
	Timestamp string `json:"ts"`
}
