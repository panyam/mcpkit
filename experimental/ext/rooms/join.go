package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// TokenMinter mints relay tokens for pages. The relay decides what a token
// looks like, so this module only names the call. The massrelay module's
// Signer implements it.
type TokenMinter interface {
	// MintRoomToken returns a token that admits subject to room and to no
	// other room, and the time the token expires.
	MintRoomToken(ctx context.Context, subject, room string) (token string, expiresAt time.Time, err error)
}

// JoinOptions configure JoinHandler. Subject, RoomOf, Minter and RelayURL are
// required.
type JoinOptions struct {
	// Subject names the page's user from the host's own authentication of r,
	// such as its session cookie. The relay reports this subject as the page's
	// user, and pairing compares it with an agent's subject, so it must come
	// from something the host verified. An error is answered 401 with the
	// error's text.
	Subject func(r *http.Request) (string, error)

	// RoomOf decides the one room this request may join, for example from a
	// document id in the query. It is the host's access check: return an
	// error when the user may not open that room, and it is answered 403 with
	// the error's text. The handler never reads r's body, so RoomOf may.
	RoomOf func(r *http.Request) (string, error)

	// Minter signs the token. It is called after Subject and RoomOf succeed,
	// and a failure is answered 500 without its text, which may name keys.
	Minter TokenMinter

	// RelayURL is the relay's base URL, as the page passes it to
	// joinToolRoom. It may be relative to the join endpoint, such as
	// "/relay", and the TypeScript helper resolves it against the endpoint's
	// URL.
	RelayURL string

	// BridgeSubjects are the relay subjects the host's bridges join as. The
	// page sets them as trustBridges, so it answers no other peer.
	BridgeSubjects []string
}

// JoinResponse is the JSON body JoinHandler answers with.
type JoinResponse struct {
	Room           string    `json:"room"`
	RelayURL       string    `json:"relayUrl"`
	Token          string    `json:"token"`
	BridgeSubjects []string  `json:"bridgeSubjects"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

// JoinHandler returns the endpoint a page calls to join its room. It answers
// POST only, with a JoinResponse carrying a token that admits the caller to
// the one room RoomOf chose. Every response is marked no-store, since it
// carries a token. The page calls it again to refresh the token before
// expiresAt, so it should stay cheap.
//
// The handler does not authenticate anyone itself. Mount it behind the
// host's own authentication, which Subject then reads.
//
// It panics when a required option is missing, since that is a wiring
// mistake rather than a request error.
func JoinHandler(o JoinOptions) http.Handler {
	switch {
	case o.Subject == nil:
		panic("rooms: JoinHandler: JoinOptions.Subject is required")
	case o.RoomOf == nil:
		panic("rooms: JoinHandler: JoinOptions.RoomOf is required")
	case o.Minter == nil:
		panic("rooms: JoinHandler: JoinOptions.Minter is required")
	case o.RelayURL == "":
		panic("rooms: JoinHandler: JoinOptions.RelayURL is required")
	}
	bridges := append([]string{}, o.BridgeSubjects...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			joinError(w, http.StatusMethodNotAllowed, "join with POST")
			return
		}
		subject, err := o.Subject(r)
		if err == nil && subject == "" {
			err = errors.New("no user")
		}
		if err != nil {
			joinError(w, http.StatusUnauthorized, err.Error())
			return
		}
		room, err := o.RoomOf(r)
		if err == nil && room == "" {
			err = errors.New("no room")
		}
		if err != nil {
			joinError(w, http.StatusForbidden, err.Error())
			return
		}
		token, exp, err := o.Minter.MintRoomToken(r.Context(), subject, room)
		if err != nil {
			slog.ErrorContext(r.Context(), "rooms: JoinHandler: minting a relay token", "room", room, "err", err)
			joinError(w, http.StatusInternalServerError, "could not mint a relay token")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JoinResponse{
			Room: room, RelayURL: o.RelayURL, Token: token, BridgeSubjects: bridges, ExpiresAt: exp,
		})
	})
}

func joinError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
