package massrelay

import (
	"net/http"

	"github.com/panyam/massrelay/services"
	relaymw "github.com/panyam/massrelay/web/middleware"
	relayserver "github.com/panyam/massrelay/web/server"
	mw "github.com/panyam/servicekit/middleware"

	"github.com/panyam/mcpkit/experimental/ext/rooms"
)

// DefaultBridgeSubject is the relay subject a Local relay's bridge joins as.
const DefaultBridgeSubject = "rooms-bridge"

// LocalOptions configure NewLocal. Every field is optional.
type LocalOptions struct {
	// Signer mints the pages' tokens, and the relay accepts only its tokens.
	// Default NewSigner(SignerOptions{}).
	Signer *Signer
	// Origins are the hosts whose pages may open the relay's WebSocket,
	// spelled as the app is served, such as "localhost" or "app.example.com".
	// Empty allows any origin.
	Origins []string
	// BridgeSubject is the subject the bridge joins as. Pages trust it
	// through the join response's bridgeSubjects. Default DefaultBridgeSubject.
	BridgeSubject string
}

// Local is a massrelay relay served by the app itself, with the bridge joined
// in-process and the signer for its pages' tokens. It is what a web app needs
// to let agents drive its own pages:
//
//	local, err := massrelay.NewLocal(massrelay.LocalOptions{})
//	bridge := rooms.New(local.Transport())
//	bridge.Register(srv)
//	mux.Handle("/relay/", http.StripPrefix("/relay", local.Relay()))
//	mux.Handle("POST /rooms/join", local.JoinHandler(rooms.JoinOptions{
//		Subject: userOf, RoomOf: roomOf, RelayURL: "/relay",
//	}))
//
// The relay requires a token on every connection and accepts only the
// signer's. It applies no connection rate limits, since every page of a local
// app may share one address. Put a limit in front of it when it faces the
// internet.
type Local struct {
	app     *relayserver.RelayApp
	signer  *Signer
	subject string
}

// NewLocal builds the relay. The RELAY_* environment variables a standalone
// relay reads for auth, admin and origins do not apply to it.
func NewLocal(o LocalOptions) (*Local, error) {
	l := &Local{signer: o.Signer, subject: o.BridgeSubject}
	if l.signer == nil {
		s, err := NewSigner(SignerOptions{})
		if err != nil {
			return nil, err
		}
		l.signer = s
	}
	if l.subject == "" {
		l.subject = DefaultBridgeSubject
	}
	// NewRelayApp reads RELAY_* from the environment. Everything that decides
	// who gets in is replaced here, so no variable can open this relay up.
	app := relayserver.NewRelayApp()
	origins := mw.NewOriginChecker(o.Origins)
	app.OriginChecker = origins
	app.AdminToken = ""
	app.PRM = nil
	app.Guard = relaymw.NewGuard(origins, nil, l.signer.Authenticator(), nil, nil)
	if err := app.Init(); err != nil {
		app.Close()
		return nil, err
	}
	l.app = app
	return l, nil
}

// Relay is the relay's HTTP handler. It serves its own paths, so mount it
// under a prefix with http.StripPrefix and give the same prefix as
// JoinOptions.RelayURL.
func (l *Local) Relay() http.Handler { return l.app }

// Transport joins rooms on this relay in-process as the bridge's subject.
// Pass it to rooms.New.
func (l *Local) Transport() rooms.RoomTransport {
	return Embedded(l.app.Service, &services.Principal{Subject: l.subject, ClientID: l.signer.ClientID()})
}

// Signer is the signer the relay trusts.
func (l *Local) Signer() *Signer { return l.signer }

// BridgeSubjects lists the subject Transport joins as, for a page's
// trustBridges.
func (l *Local) BridgeSubjects() []string { return []string{l.subject} }

// JoinHandler is rooms.JoinHandler with Minter and BridgeSubjects filled in
// from this relay when o leaves them empty.
func (l *Local) JoinHandler(o rooms.JoinOptions) http.Handler {
	if o.Minter == nil {
		o.Minter = l.signer
	}
	if o.BridgeSubjects == nil {
		o.BridgeSubjects = l.BridgeSubjects()
	}
	return rooms.JoinHandler(o)
}

// Close stops the relay's background work. Leave the bridge first.
func (l *Local) Close() { l.app.Close() }
