package roomsevents_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/experimental/ext/events"
	eventsclient "github.com/panyam/mcpkit/experimental/ext/events/clients/go"
	"github.com/panyam/mcpkit/experimental/ext/rooms"
	"github.com/panyam/mcpkit/experimental/integrations/roomsevents"
	rt "github.com/panyam/mcpkit/experimental/integrations/roomsevents/internal/roomstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stream subscribes c to name for resource and returns what arrives.
func stream(t *testing.T, c *client.Client, name, resource string) (chan events.Event, error) {
	t.Helper()
	ch := make(chan events.Event, 8)
	call, err := eventsclient.Stream(t.Context(), c, eventsclient.StreamOptions{
		EventName: name,
		Arguments: map[string]any{"resource": resource},
		OnEvent:   func(ev events.Event) { ch <- ev },
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(call.Stop)
	return ch, nil
}

func next(t *testing.T, ch chan events.Event) events.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no event arrived")
		return events.Event{}
	}
}

func none(t *testing.T, ch chan events.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %s: %s", ev.Name, ev.Data)
	case <-time.After(300 * time.Millisecond):
	}
}

// alicePage is a page alice opened, with her agents switch on, agreed to her
// attached session a.
func alicePage(t *testing.T, st *rt.Stack, a *client.Client) *rooms.Page {
	t.Helper()
	p := st.Page(t, "alice", rt.Room)
	rt.WaitListed(t, a, p.ID())
	p.SetLabel("viewer")
	p.SetAgentsAllowed(true)
	st.WaitAgreed(t, a.SessionID(), rt.Room, p.ID())
	return p
}

func TestEvents_ListOffersThreeTypesForPushAndPollOnly(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	raw, err := a.Call(t.Context(), "events/list", map[string]any{})
	require.NoError(t, err)
	var res struct {
		Events []struct {
			Name     string   `json:"name"`
			Delivery []string `json:"delivery"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw.Raw, &res))
	ours := map[string]bool{}
	for _, n := range roomsevents.EventNames {
		ours[n] = true
	}
	var names []string
	for _, e := range res.Events {
		if !ours[e.Name] {
			continue // the extension's own types, e.g. events.topology
		}
		names = append(names, e.Name)
		assert.ElementsMatch(t, []string{"push", "poll"}, e.Delivery, "%s: attachment ends with the session, so no webhook", e.Name)
	}
	assert.ElementsMatch(t, roomsevents.EventNames, names)
}

func TestEvents_AttachedAgreedSessionHearsAskOverPush(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)
	ch, err := stream(t, a, roomsevents.EventAsk, rt.Room)
	require.NoError(t, err)

	require.NoError(t, p.Ask("why is SDA flagged?"))
	ev := next(t, ch)
	var got roomsevents.AskPayload
	require.NoError(t, json.Unmarshal(ev.Data, &got))
	assert.Equal(t, "why is SDA flagged?", got.Text)
	assert.Equal(t, rt.Room, got.Resource)
	assert.Equal(t, string(p.ID()), got.Page)
	assert.Equal(t, "alice", got.User, "from the page's relay identity")
	assert.Equal(t, "viewer", got.Label)
	assert.NotEmpty(t, ev.EventID)
}

func TestEvents_PollDeliversTheSameEvents(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)

	opts := eventsclient.PollOptions{
		EventName: roomsevents.EventSelectionChanged,
		Arguments: map[string]any{"resource": rt.Room},
	}
	head, err := eventsclient.Poll(t.Context(), a, opts)
	require.NoError(t, err)
	opts.Cursor = head.Cursor()
	head.Stop()

	require.NoError(t, p.SelectionChanged(map[string]any{"net": "SDA"}))
	got := make(chan events.Event, 8)
	opts.OnEvent = func(ev events.Event) { got <- ev }
	// The server's nextPollMs outlasts a test's patience, so poll again from
	// the cursor taken before the event: its first page carries it.
	require.Eventually(t, func() bool {
		p, err := eventsclient.Poll(t.Context(), a, opts)
		if err != nil {
			return false
		}
		p.Stop()
		return len(got) > 0
	}, 3*time.Second, 50*time.Millisecond)
	var sel roomsevents.SelectionPayload
	require.NoError(t, json.Unmarshal(next(t, got).Data, &sel))
	assert.JSONEq(t, `{"net":"SDA"}`, string(sel.Selection))
}

func TestEvents_PageEventsOfItsOwnArriveAsPageEvent(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)
	ch, err := stream(t, a, roomsevents.EventPage, rt.Room)
	require.NoError(t, err)

	require.NoError(t, p.Emit("viewer.sheet_opened", map[string]any{"sheet": "s-power"}))
	var got roomsevents.PagePayload
	require.NoError(t, json.Unmarshal(next(t, ch).Data, &got))
	assert.Equal(t, "viewer.sheet_opened", got.Name)
	assert.JSONEq(t, `{"sheet":"s-power"}`, string(got.Data))
}

// The extension confirms a stream (notifications/events/active) before it runs
// OnSubscribe, so a refused stream opens and then ends with the refusal. What
// the client sees is a call that ends in an error and delivers nothing.
func TestEvents_SubscribingBeforeAttachIsRefused(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	call, err := eventsclient.Stream(t.Context(), a, eventsclient.StreamOptions{
		EventName:        roomsevents.EventAsk,
		Arguments:        map[string]any{"resource": rt.Room},
		DisableReconnect: true,
	})
	if err != nil {
		return // refused up front, which is just as good
	}
	t.Cleanup(call.Stop)
	select {
	case <-call.Done():
		require.Error(t, call.Err())
		assert.Contains(t, call.Err().Error(), "attach to")
	case <-time.After(3 * time.Second):
		t.Fatal("a stream opened before attach stayed open")
	}
}

// A page's events reach only sessions it agreed to, the same rule as calls.
func TestEvents_OnlySessionsThePageAgreedToHearIt(t *testing.T) {
	st := rt.New(t)
	alice := st.Agent(t, "alice")
	bob := st.Agent(t, "bob")
	rt.Attach(t, alice, rt.Room)
	rt.Attach(t, bob, rt.Room)
	p := alicePage(t, st, alice)

	aliceCh, err := stream(t, alice, roomsevents.EventAsk, rt.Room)
	require.NoError(t, err)
	bobCh, err := stream(t, bob, roomsevents.EventAsk, rt.Room)
	require.NoError(t, err, "bob is attached, so he may subscribe")

	require.NoError(t, p.Ask("for my agents only"))
	next(t, aliceCh)
	none(t, bobCh)
}

func TestEvents_AnotherRoomsSubscriptionHearsNothing(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	rt.Attach(t, a, "design:other")
	p := alicePage(t, st, a)
	other, err := stream(t, a, roomsevents.EventAsk, "design:other")
	require.NoError(t, err)

	require.NoError(t, p.Ask("hi"))
	none(t, other)
}

// Detaching ends what the session hears, even on a stream it already holds.
func TestEvents_DetachedSessionStopsHearing(t *testing.T) {
	st := rt.New(t)
	a := st.Agent(t, "alice")
	rt.Attach(t, a, rt.Room)
	p := alicePage(t, st, a)
	ch, err := stream(t, a, roomsevents.EventAsk, rt.Room)
	require.NoError(t, err)

	res, err := a.ToolCallFull(t.Context(), "detach", map[string]any{"resource": rt.Room})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NoError(t, p.Ask("anyone?"))
	none(t, ch)
}
