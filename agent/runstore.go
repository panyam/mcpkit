package agent

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
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
//
// Atomicity contract: CreateRun with an explicit RunID and ForkRun are
// all-or-nothing. On error, no run observable at the requested ID came
// into existence; a run exists at that ID only because some create or
// fork fully committed. This is what makes caller-chosen IDs safe
// idempotency keys for retry loops (see ForkRunRequest). Implementers
// must uphold it — a partially-forked run that reports Created=false
// to a retry is a contract violation, not a quirk.
type RunStore interface {
	CreateRun(ctx context.Context, req CreateRunRequest) (CreateRunResponse, error)
	AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error)
	AppendEvents(ctx context.Context, req AppendEventsRequest) (AppendEventsResponse, error)
	LoadRun(ctx context.Context, req LoadRunRequest) (LoadRunResponse, error)
	ForkRun(ctx context.Context, req ForkRunRequest) (ForkRunResponse, error)

	// ListRuns enumerates stored runs as lightweight RunInfo (no message
	// bodies), for a session picker or a dashboard. Paged via an opaque
	// cursor; NextCursor is empty when the last page was returned. A
	// non-agent consumer (a poller, a UI) wants this too, which is why it
	// returns protocol/data objects, not model-facing ones.
	ListRuns(ctx context.Context, req ListRunsRequest) (ListRunsResponse, error)
}

// RunInfo is the header of a run — identity, lineage, and counts — without
// the message or event bodies, so a listing stays cheap. MessageCount is
// the current length of the message log; the fork lineage (ParentID,
// ForkPoint) is the session tree a picker renders.
type RunInfo struct {
	ID           string    `json:"id"`
	ParentID     string    `json:"parentId,omitempty"`
	ForkPoint    int       `json:"forkPoint,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	MessageCount int       `json:"messageCount"`
}

// ListRunsRequest pages the run listing. Cursor is empty for the first
// page and echoes a prior response's NextCursor thereafter; its content is
// backend-defined and opaque. Limit caps the page (zero means the
// backend's default).
type ListRunsRequest struct {
	Cursor string
	Limit  int
}

// ListRunsResponse carries one page. NextCursor is empty on the last page.
// Ordering is best-effort newest-first where the backend can (in-memory,
// gorm); the Redis backend lists in scan order (unordered) — documented on
// its implementation.
type ListRunsResponse struct {
	Runs       []RunInfo
	NextCursor string
}

// DefaultListRunsLimit bounds a page when ListRunsRequest.Limit is zero.
const DefaultListRunsLimit = 50

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

	// ForkPoint is the number of ParentID's messages this run was
	// forked with — the fork position as a backend-neutral message
	// count (never a timestamp: the fork's wall-clock moment is this
	// run's CreatedAt; never a storage sequence value either). Zero for
	// runs created directly. Lineage metadata for surfaces (rewind
	// pickers, history UIs): nothing reconstructs history from it,
	// since every run owns its full copy.
	ForkPoint int `json:"forkPoint,omitempty"`

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
//
// Stamping rule: implementations set Message.Timestamp to their own
// clock for every appended message whose Timestamp is zero, and
// preserve non-zero values verbatim (caller wins — a surface can stamp
// the user message at keypress). One boundary, one rule: code that
// constructs messages never needs to remember to stamp.
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
//
// A non-empty NewRunID is also the fork's idempotency key. Forks are
// all-or-nothing (see RunStore), so a retry loop that mints one
// deterministic ID (a session-scoped name, a ULID) and reuses it
// across attempts converges: retry after a failure finds nothing at
// the ID and forks clean; retry after an unobserved success gets
// Created=false against a complete fork, confirmable by loading it and
// checking ParentID.
type ForkRunRequest struct {
	RunID    string
	NewRunID string

	// AtMessage forks from an earlier point: a positive value copies
	// only the first AtMessage messages (checkpoint/rewind semantics);
	// zero or negative copies everything, today's behavior. A value at
	// or beyond the source's length clamps to a full copy. The source's
	// length is observed when the fork starts; appends racing the fork
	// land after the cut.
	//
	// Event-log handling: the audit stream is not sliceable by message
	// index, so a partial fork copies NO events — only a full copy
	// carries the event log across. The fork's message log is complete
	// either way, which is all resume needs.
	AtMessage int
}

// ForkRunResponse identifies the fork. Found is false when the source
// run does not exist; Created is false when NewRunID was claimed and
// already existed (no copy happens). On success both are true, RunID
// names the new run (ParentID records the lineage), and ForkPoint is
// the message count actually copied — the resolved fork position after
// clamping, also persisted on the fork's Run.
type ForkRunResponse struct {
	RunID     string
	Found     bool
	Created   bool
	ForkPoint int
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
	forkPoint int
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

// AppendMessages implements RunStore, stamping zero Timestamps per the
// AppendMessagesRequest rule.
func (s *InMemoryRunStore) AppendMessages(ctx context.Context, req AppendMessagesRequest) (AppendMessagesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.runs[req.RunID]
	if !ok {
		return AppendMessagesResponse{Found: false}, nil
	}
	start := len(e.messages)
	e.messages = append(e.messages, req.Messages...)
	now := time.Now()
	for i := start; i < len(e.messages); i++ {
		if e.messages[i].Timestamp.IsZero() {
			e.messages[i].Timestamp = now
		}
	}
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
			ForkPoint: e.forkPoint,
			CreatedAt: e.createdAt,
			Messages:  slices.Clone(e.messages),
			Events:    slices.Clone(e.events),
		},
		Found: true,
	}, nil
}

// ListRuns implements RunStore, newest-first. The cursor is a decimal
// offset into the ordered set — fine for an in-memory store whose set is
// stable within a process.
func (s *InMemoryRunStore) ListRuns(ctx context.Context, req ListRunsRequest) (ListRunsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]RunInfo, 0, len(s.runs))
	for id, e := range s.runs {
		infos = append(infos, RunInfo{
			ID:           id,
			ParentID:     e.parentID,
			ForkPoint:    e.forkPoint,
			CreatedAt:    e.createdAt,
			MessageCount: len(e.messages),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if !infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].CreatedAt.After(infos[j].CreatedAt)
		}
		return infos[i].ID < infos[j].ID
	})

	offset := 0
	if req.Cursor != "" {
		if n, err := strconv.Atoi(req.Cursor); err == nil && n > 0 {
			offset = n
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultListRunsLimit
	}
	if offset >= len(infos) {
		return ListRunsResponse{}, nil
	}
	end := offset + limit
	var next string
	if end < len(infos) {
		next = strconv.Itoa(end)
	} else {
		end = len(infos)
	}
	return ListRunsResponse{Runs: infos[offset:end], NextCursor: next}, nil
}

// ForkRun implements RunStore. The fork gets cloned logs, so parent and
// fork diverge independently after the copy; see ForkRunRequest for the
// AtMessage cut and event-log semantics.
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
	n := len(src.messages)
	if req.AtMessage > 0 && req.AtMessage < n {
		n = req.AtMessage
	}
	var events []Event
	if n == len(src.messages) {
		events = slices.Clone(src.events)
	}
	s.runs[id] = &runEntry{
		parentID:  req.RunID,
		forkPoint: n,
		createdAt: time.Now(),
		messages:  slices.Clone(src.messages[:n]),
		events:    events,
	}
	return ForkRunResponse{RunID: id, Found: true, Created: true, ForkPoint: n}, nil
}
