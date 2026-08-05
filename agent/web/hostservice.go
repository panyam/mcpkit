// Package web exposes agent/host.App over a Connect RPC bridge with a live
// event stream, so a browser can drive the same surface-agnostic host the
// terminal agentchat drives (issue 1196, epic 1193). The bridge is a thin
// projection: it holds only an *App and translates each RPC to an App method.
// No web or proto type leaks back into agent/host (agent/CONSTRAINTS A4/A6);
// the host stays surface-agnostic and this module owns all the web wiring.
package web

import (
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	"github.com/panyam/mcpkit/agent/host"
	agentwebv1 "github.com/panyam/mcpkit/agent/web/gen/go/mcpkit/agentweb/v1"
	"github.com/panyam/mcpkit/agent/web/gen/go/mcpkit/agentweb/v1/agentwebv1connect"
	"github.com/panyam/mcpkit/core"
)

// HostService is the Connect handler that bridges HostService RPCs to an App.
// Embedding UnimplementedHostServiceHandler keeps it forward-compatible when the
// proto gains methods.
type HostService struct {
	agentwebv1connect.UnimplementedHostServiceHandler
	app *host.App
}

// NewHostService wraps app as a Connect handler. The App is shared: a terminal
// surface and any number of web clients can be attached to the same one at
// once, which is the multi-surface point.
func NewHostService(app *host.App) *HostService { return &HostService{app: app} }

// Watch streams the host event log: the retained backlog replayed from offset 0
// (App.Subscribe), then live events, until the client disconnects. The derived
// ctx is cancelled on return so the drain goroutine and its Subscription exit
// even when Send fails before the request ctx is observed.
func (s *HostService) Watch(ctx context.Context, _ *connect.Request[agentwebv1.WatchRequest], stream *connect.ServerStream[agentwebv1.Frame]) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Open the stream immediately with a ready sentinel (empty kind). The
	// Connect protocol flushes response headers on the first Send, so without
	// this a client attaching to an idle session would block on Watch until the
	// first real event. Clients skip a frame whose kind is empty.
	if err := stream.Send(&agentwebv1.Frame{}); err != nil {
		return err
	}
	for ev := range s.app.Subscribe(ctx) {
		if err := stream.Send(eventToFrame(ev)); err != nil {
			return err
		}
	}
	return nil
}

// Submit runs one conversational turn. The turn's events stream on Watch; this
// returns when the turn finishes. A failed turn maps to a Connect error (the
// same failure is also announced as a HostTurnFailed frame on Watch).
func (s *HostService) Submit(ctx context.Context, req *connect.Request[agentwebv1.SubmitRequest]) (*connect.Response[agentwebv1.SubmitResponse], error) {
	if err := s.app.RunTurn(ctx, req.Msg.GetInput()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentwebv1.SubmitResponse{}), nil
}

// Dispatch runs a slash command and returns its CmdResult as {kind, json}. An
// unknown command is CodeInvalidArgument (a client can retry it as a turn); any
// other command error is CodeInternal.
func (s *HostService) Dispatch(ctx context.Context, req *connect.Request[agentwebv1.DispatchRequest]) (*connect.Response[agentwebv1.DispatchResponse], error) {
	res, err := s.app.Dispatch(ctx, req.Msg.GetLine())
	if errors.Is(err, host.ErrUnknownCommand) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentwebv1.DispatchResponse{
		Kind:    string(res.Kind),
		Payload: marshalOrMinimal(res, host.HostEventKind(res.Kind)),
		Quit:    res.Quit,
	}), nil
}

// RespondToAsk answers a pending elicitation by the AskID a client read off a
// Frame. First responder wins; an unknown or already-answered ask is
// CodeFailedPrecondition (app state, not a transport failure). An empty By
// defaults to "web" so the resolved frame names the surface.
func (s *HostService) RespondToAsk(_ context.Context, req *connect.Request[agentwebv1.RespondToAskRequest]) (*connect.Response[agentwebv1.RespondToAskResponse], error) {
	var result core.ElicitationResult
	if raw := req.Msg.GetResult(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	by := req.Msg.GetBy()
	if by == "" {
		by = "web"
	}
	// ask_id carries the event-log offset the ask was announced at (E2's
	// offset-based RespondToAsk); cast int64→int at the boundary. Resolution
	// rides the log barrier, so a stale or already-answered offset returns the
	// log's ErrAlreadyResolved / ErrOffsetOutOfRange (app state, reported as a
	// failed precondition, not a transport failure).
	if err := s.app.RespondToAsk(int(req.Msg.GetAskId()), result, by); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&agentwebv1.RespondToAskResponse{}), nil
}

// ListSessions returns one page of persisted sessions. No RunStore configured is
// CodeFailedPrecondition (persistence is off, an app-state fact).
func (s *HostService) ListSessions(ctx context.Context, req *connect.Request[agentwebv1.ListSessionsRequest]) (*connect.Response[agentwebv1.ListSessionsResponse], error) {
	page, err := s.app.SessionsPage(ctx, req.Msg.GetCursor())
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	runs, _ := json.Marshal(page.Runs)
	return connect.NewResponse(&agentwebv1.ListSessionsResponse{
		Runs:        runs,
		HasMore:     page.HasMore,
		ActiveRunId: s.app.RunID(),
	}), nil
}

// GetStatus returns the active model label and run id — the status-line read.
func (s *HostService) GetStatus(_ context.Context, _ *connect.Request[agentwebv1.GetStatusRequest]) (*connect.Response[agentwebv1.GetStatusResponse], error) {
	return connect.NewResponse(&agentwebv1.GetStatusResponse{
		ModelLabel: s.app.ModelLabel(),
		RunId:      s.app.RunID(),
	}), nil
}
