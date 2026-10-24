package massrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pb "github.com/panyam/massrelay/gen/go/massrelay/v1/models"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// DialOption configures Dial.
type DialOption func(*remote)

// WithDialer sets the WebSocket dialer, e.g. for TLS settings or a proxy.
func WithDialer(d *websocket.Dialer) DialOption {
	return func(r *remote) { r.dialer = d }
}

// WithUsername sets the display name the relay shows other peers. The bridge
// never reads it; identity comes from the token.
func WithUsername(name string) DialOption {
	return func(r *remote) { r.username = name }
}

// DefaultSendRate is the messages a second a dialled membership sends at
// most. The relay drops what one WebSocket sends past 30 a second without
// telling the sender, so this stays under it.
const DefaultSendRate = 25

// WithSendRate sets how many messages a second each dialled membership sends,
// with a burst of the same size. Send waits for its turn, or fails when its
// context ends first. Match it to the relay's StreamConfig.MaxMessageRate if
// that was changed; 0 turns pacing off.
func WithSendRate(perSecond float64) DialOption {
	return func(r *remote) { r.sendRate = perSecond }
}

// Dial returns a RoomTransport that joins rooms on the relay at baseURL
// (http, https, ws or wss, with any path prefix the relay is mounted under)
// over its WebSocket endpoint, `<baseURL>/ws/v1/<room>/sync`. Each Join opens
// its own connection, since the relay allows one room per connection.
//
// token is the relay token the peer joins with. The relay validates it and
// reports its `sub` as the peer's Subject; it travels as a Bearer header.
// Empty joins anonymously.
func Dial(baseURL, token string, opts ...DialOption) rooms.RoomTransport {
	r := &remote{base: baseURL, token: token, dialer: websocket.DefaultDialer, sendRate: DefaultSendRate}
	for _, o := range opts {
		o(r)
	}
	return r
}

type remote struct {
	base     string
	token    string
	dialer   *websocket.Dialer
	username string
	sendRate float64
}

// envelope is servicekit's grpcws control message, which carries every frame
// on the relay's WebSocket: data (a protojson CollabAction or CollabEvent),
// error, stream_end, and ping/pong.
type envelope struct {
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
	PingID int64           `json:"pingId,omitempty"`
}

func (r *remote) Join(ctx context.Context, room string) (rooms.Room, error) {
	u, err := wsURL(r.base, room)
	if err != nil {
		return nil, err
	}
	hdr := http.Header{}
	if r.token != "" {
		hdr.Set("Authorization", "Bearer "+r.token)
	}
	conn, resp, err := r.dialer.DialContext(ctx, u, hdr)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("massrelay: dial %s: %s: %w", room, resp.Status, err)
		}
		return nil, fmt.Errorf("massrelay: dial %s: %w", room, err)
	}
	c := &wsConn{conn: conn}
	if err := c.sendAction(joinAction(room, r.username, 2)); err != nil {
		conn.Close()
		return nil, err
	}

	// The relay answers a join with RoomJoined or an ErrorEvent; anything
	// else before it (a ping) is handled in passing.
	var joined *pb.RoomJoined
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetReadDeadline(deadline)
	for joined == nil {
		ev, err := c.read()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("massrelay: join %s: %w", room, err)
		}
		if ev == nil {
			continue
		}
		if joined, err = joinedFrom(room, ev); err != nil {
			conn.Close()
			return nil, err
		}
	}
	_ = conn.SetReadDeadline(time.Time{})

	m := newMember(joined)
	var once sync.Once
	m.leave = func() {
		once.Do(func() {
			_ = c.sendAction(&pb.CollabAction{Action: &pb.CollabAction_Leave{Leave: &pb.LeaveRoom{Reason: "left"}}})
			conn.Close()
		})
	}
	var pace *rate.Limiter
	if r.sendRate > 0 {
		pace = rate.NewLimiter(rate.Limit(r.sendRate), max(1, int(r.sendRate)))
	}
	m.send = func(ctx context.Context, action *pb.CollabAction) error {
		if pace != nil {
			if err := pace.Wait(ctx); err != nil {
				return err
			}
		}
		// The relay stamps the sender from the connection; the client id
		// here is ignored and left empty.
		action.ClientId = ""
		return c.sendAction(action)
	}
	go func() {
		defer m.close()
		for {
			ev, err := c.read()
			if err != nil {
				return
			}
			if ev != nil {
				m.handle(ev)
			}
		}
	}()
	return m, nil
}

// wsConn serializes writes, since the read loop answers pings while Send
// writes messages.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *wsConn) write(env envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(env)
}

func (c *wsConn) sendAction(a *pb.CollabAction) error {
	data, err := protojson.Marshal(a)
	if err != nil {
		return err
	}
	return c.write(envelope{Type: "data", Data: data})
}

// read returns the next CollabEvent, nil for a frame that carried none (a
// ping, answered here), or an error when the stream has ended.
func (c *wsConn) read() (*pb.CollabEvent, error) {
	var env envelope
	if err := c.conn.ReadJSON(&env); err != nil {
		return nil, err
	}
	switch env.Type {
	case "data":
		ev := &pb.CollabEvent{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(env.Data, ev); err != nil {
			return nil, nil
		}
		return ev, nil
	case "ping":
		return nil, c.write(envelope{Type: "pong", PingID: env.PingID})
	case "error":
		return nil, errors.New("massrelay: " + env.Error)
	case "stream_end":
		return nil, errors.New("massrelay: stream ended")
	}
	return nil, nil
}

func wsURL(base, room string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("massrelay: relay url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("massrelay: relay url %q: scheme must be http, https, ws or wss", base)
	}
	// The room is one path segment, so a "/" in it travels as %2F.
	raw := strings.TrimSuffix(u.EscapedPath(), "/") + "/ws/v1/" + url.PathEscape(room) + "/sync"
	if u.Path, err = url.PathUnescape(raw); err != nil {
		return "", err
	}
	u.RawPath = raw
	return u.String(), nil
}
