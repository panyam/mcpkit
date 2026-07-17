package agent

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// RunStore is the persistence seam for runs: append-only logs of the
// messages (and optionally events) a session accumulates across turns.
// Surfaces persist TurnResult.Messages after each turn; resume is
// LoadRun followed by Run over the loaded messages, and fork is ForkRun
// followed by divergence — both cheap because the Runner is stateless
// over history. The Runner itself never touches a RunStore.
//
// API shape follows the gRPC-style convention pinned in
// stores/STORAGE_SEAMS.md:
//
//	Method(ctx context.Context, req XRequest) (XResponse, error)
//
// ctx threads cancellation, deadlines, and trace context.
// Application-level state (Found, Created) lives on the response; error
// is reserved for storage-layer failures (connection drops, corrupt
// records). In particular, an unknown RunID is app-state (Found=false),
// never an error.
//
// The interface and its in-memory default live in agent/ because every
// method traffics in agent.Message / agent.Event: hosting them in the
// root stores/ package would force a root→agent dependency (constraint
// A6 corollary). Durable backends are sibling modules (agent/store/...)
// so their dependencies stay out of this module.
type RunStore interface {
	CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error)
	AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error)
	AppendEvents(ctx context.Context, req AppendEventsRequest) (AppendEventsResponse, error)
	LoadRun(ctx context.Context, req LoadRunRequest) (LoadRunResponse, error)
	ForkRun(ctx context.Context, req ForkRunRequest) (ForkRunResponse, error)
}

// Run is one persisted run: identity, fork lineage, and the append-only
// logs. Messages is the conversation history a resume feeds back into
// Runner.Run; Events is the optional replay/audit stream. Both element
// types are wire-serializable by constraint A2, so Run itself marshals
// cleanly through encoding/json — the property durable backends rely on.
type Run struct {
	ID string `json:"id"`

	// ParentID is the run this one was forked from; empty for runs
	// created directly.
	ParentID string `json:"parentId,omitempty"`

	CreatedAt time.Time `json:"createdAt"`

	Messages []Message `json:"messages"`
	Events   []Event   `json:"events,omitempty"`
}

// CreateRunRequest starts a new empty run. RunID is optional: empty asks
// the store to generate a unique ID; non-empty claims a caller-chosen
// name (a session name, a ticket ID). Claiming an ID that already exists
// is not an overwrite — the store leaves the existing run intact and
// reports Created=false.
type CreateRunRequest struct {
	RunID string
}

// CreateRunResponse carries the run's identity. Created is false when
// the requested RunID already existed (the existing run is untouched);
// stores always report Created=true for generated IDs.
type CreateRunResponse struct {
	RunID   string
	Created bool
}

// AppendMessagesRequest appends messages to a run's log in order.
// Callers pass exactly the entries a turn added (the user message plus
// TurnResult.Messages) so the stored log threads the same way in-process
// history does.
type AppendMessagesRequest struct {
	RunID    string
	Messages []Message
}

// AppendMessagesResponse reports whether the run existed. Found=false
// means nothing was written — the caller appended to a run it never
// created (or one that was pruned), which is a caller bug to surface,
// not a storage fault.
type AppendMessagesResponse struct {
	Found bool
}

// AppendEventsRequest appends events to a run's audit/replay log. The
// event log is optional and independent of the message log: a store
// accepts events for any existing run whether or not the surface also
// persists messages.
type AppendEventsRequest struct {
	RunID  string
	Events []Event
}

// AppendEventsResponse reports whether the run existed; Found=false
// means nothing was written (see AppendMessagesResponse).
type AppendEventsResponse struct {
	Found bool
}

// LoadRunRequest fetches a run by ID.
type LoadRunRequest struct {
	RunID string
}

// LoadRunResponse carries the run when Found. The returned Run is the
// caller's to keep: implementations return copies (or freshly decoded
// values), never aliases into store-internal state, so mutating
// Run.Messages cannot corrupt the log.
type LoadRunResponse struct {
	Run   Run
	Found bool
}

// ForkRunRequest copies an existing run's logs into a new run so the
// copy can diverge. NewRunID follows CreateRunRequest.RunID semantics:
// empty generates a unique ID, non-empty claims a caller-chosen one.
type ForkRunRequest struct {
	RunID    string
	NewRunID string
}

// ForkRunResponse identifies the fork. Found is false when the source
// run does not exist; Created is false when NewRunID was claimed and
// already existed (no copy happens). On success both are true and RunID
// names the new run, whose ParentID records the lineage.
type ForkRunResponse struct {
	RunID   string
	Found   bool
	Created bool
}

// InMemoryRunStore is the default RunStore: a mutex-guarded map, useful
// for tests and single-process sessions that only need in-lifetime
// resume/fork. It is safe for concurrent use. Nothing survives process
// exit — durable deployments swap in a sibling backend behind the same
// interface.
type InMemoryRunStore struct {
	mu   sync.Mutex
	runs map[string]*runEntry
	seq  int
}

// runEntry consolidates one run's state (constraint C2: one entry struct
// instead of parallel same-keyed maps).
type runEntry struct {
	parentID  string
	createdAt time.Time
	messages  []Message
	events    []Event
}

// NewInMemoryRunStore returns an empty in-memory RunStore.
func NewInMemoryRunStore() *InMemoryRunStore {
	return &InMemoryRunStore{runs: map[string]*runEntry{}}
}

// CreateRun implements RunStore. Generated IDs are sequential
// ("run-1", "run-2", ...) — deterministic on purpose, since an in-memory
// store never shares an ID space across processes.
func (s *InMemoryRunStore) CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := req.RunID
	if id == "" {
		s.seq++
		id = fmt.Sprintf("run-%d", s.seq)
	} else if _, ok := s.runs[id]; ok {
		return CreateRunResponse{RunID: id, Created: false}, nil
	}
	s.runs[id] = &runEntry{createdAt: time.Now()}
	return CreateRunResponse{RunID: id, Created: true}, nil
}

// AppendMessages implements RunStore.
func (s *InMemoryRunStore) AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return AppendMessagesResponse{Found: false}, nil
	}
	e.messages = append(e.messages, req.Messages...)
	return AppendMessagesResponse{Found: true}, nil
}

// AppendEvents implements RunStore.
func (s *InMemoryRunStore) AppendEvents(ctx context.Context, req AppendEventsRequest) (AppendEventsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return AppendEventsResponse{Found: false}, nil
	}
	e.events = append(e.events, req.Events...)
	return AppendEventsResponse{Found: true}, nil
}

// LoadRun implements RunStore. The returned Run's slices are clones, so
// callers may append to or mutate them freely.
func (s *InMemoryRunStore) LoadRun(ctx context.Context, req LoadRunRequest) (LoadRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return LoadRunResponse{}, nil
	}
	return LoadRunResponse{
		Run: Run{
			ID:        req.RunID,
			ParentID:  e.parentID,
			CreatedAt: e.createdAt,
			Messages:  slices.Clone(e.messages),
			Events:    slices.Clone(e.events),
		},
		Found: true,
	}, nil
}

// ForkRun implements RunStore. The fork gets cloned logs, so parent and
// fork diverge independently after the copy.
func (s *InMemoryRunStore) ForkRun(ctx context.Context, req ForkRunRequest) (ForkRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.runs[req.RunID]
	if !ok {
		return ForkRunResponse{}, nil
	}
	id := req.NewRunID
	if id == "" {
		s.seq++
		id = fmt.Sprintf("run-%d", s.seq)
	} else if _, exists := s.runs[id]; exists {
		return ForkRunResponse{RunID: id, Found: true, Created: false}, nil
	}
	s.runs[id] = &runEntry{
		parentID:  req.RunID,
		createdAt: time.Now(),
		messages:  slices.Clone(src.messages),
		events:    slices.Clone(src.events),
	}
	return ForkRunResponse{RunID: id, Found: true, Created: true}, nil
}
