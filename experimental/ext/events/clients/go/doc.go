// Package eventsclient is the Go-side SDK for the MCP Events extension.
//
// Two pieces, designed to compose:
//
//   - Subscription manages an events/subscribe lifecycle with automatic
//     TTL refresh per the spec's soft-state model. Construct via Subscribe;
//     stop via Stop or by cancelling the parent context.
//   - Receiver[Data] is a typed webhook receiver. Implements http.Handler
//     so it can be hung off any mux. Verifies signatures (auto-detects
//     X-MCP-* vs webhook-* per the registry's header mode), decodes the
//     wire envelope's Data field into the typed Data parameter, and
//     delivers Event[Data] values on the Events() channel.
//
// Quickstart:
//
//	c := client.NewClient(...)
//	if err := c.Connect(); err != nil { ... }
//
//	type AlertData struct{ Severity, Service, Message string }
//
//	recv := eventsclient.NewReceiver[AlertData]("")
//	httpServer := httptest.NewServer(recv)
//	defer httpServer.Close()
//	defer recv.Close()
//
//	sub, err := eventsclient.Subscribe(ctx, c, eventsclient.SubscribeOptions{
//	    EventName:   "alert.fired",
//	    CallbackURL: httpServer.URL,
//	})
//	if err != nil { ... }
//	defer sub.Stop()
//	recv.SetSecret(sub.Secret()) // adopt server-assigned secret
//
//	for ev := range recv.Events() {
//	    fmt.Printf("alert: %+v\n", ev.Data)
//	}
//
// Mirrors the Python WebhookSubscription helper in
// experimental/ext/events/clients/python/events_client.py — same lifecycle, same TTL
// refresh / "subscription not found" recovery semantics, with typed
// payload delivery added on top.
package eventsclient
