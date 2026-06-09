package tasks_test

// SEP-414 P6 — ext/tasks span links (issue 659).
//
// Each test wires a recording core.TracerProvider into BOTH the server
// (so the inbound dispatch span exists and SpanFromContext returns it
// inside v2 method handlers) AND tasks.Register (so spawnGoAsyncTask
// can emit the task.execute root span). The fake TP records every
// StartSpan / StartSpanLinked / AddLink / SetAttribute / RecordError /
// End call so assertions can inspect the full shape without depending
// on a real OTel SDK exporter — keeps ext/tasks dep-free of ext/otel.
//
// End-to-end materialization against a real exporter is proven by
// ext/otel/provider_test.go (the StartSpanLinked / AddLink / new-root
// scrub paths all have OTel-SDK-backed coverage). This file proves
// ext/tasks calls the contract correctly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	. "github.com/panyam/mcpkit/server"
	tasks "github.com/panyam/mcpkit/ext/tasks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Recording fake TracerProvider ------------------------------------------

type recordedCall struct {
	name      string
	links     []core.Link
	attrs     []core.Attribute
	rootSpan  bool // ctx was marked WithNewRootSpan when StartSpan(Linked) ran
	parentTC  core.TraceContext
	useLinked bool // came in via StartSpanLinked rather than plain StartSpan
}

type fakeSpan struct {
	mu       sync.Mutex
	name     string
	attrs    map[string]string
	errors   []error
	links    []core.Link
	ended    bool
	endCount int
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	s.endCount++
}
func (s *fakeSpan) SetAttribute(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]string{}
	}
	s.attrs[k] = v
}
func (s *fakeSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors = append(s.errors, err)
}
func (s *fakeSpan) AddLink(l core.Link) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = append(s.links, l)
}

func (s *fakeSpan) attr(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[k]
}
func (s *fakeSpan) errCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.errors)
}
func (s *fakeSpan) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}
func (s *fakeSpan) endTimes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endCount
}
func (s *fakeSpan) addLinkCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.links)
}

// fakeTP implements core.TracerProvider AND core.LinkedTracerProvider.
// Records every call so tests can inspect what ext/tasks asked for.
type fakeTP struct {
	mu    sync.Mutex
	calls []recordedCall
	spans []*fakeSpan
}

func (p *fakeTP) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	return p.record(ctx, name, nil, attrs, false)
}

func (p *fakeTP) StartSpanLinked(ctx context.Context, name string, links []core.Link, attrs ...core.Attribute) (context.Context, core.Span) {
	return p.record(ctx, name, links, attrs, true)
}

func (p *fakeTP) record(ctx context.Context, name string, links []core.Link, attrs []core.Attribute, useLinked bool) (context.Context, core.Span) {
	sp := &fakeSpan{name: name}
	p.mu.Lock()
	idx := len(p.spans)
	p.calls = append(p.calls, recordedCall{
		name:      name,
		links:     append([]core.Link(nil), links...),
		attrs:     append([]core.Attribute(nil), attrs...),
		rootSpan:  core.IsNewRootSpanRequested(ctx),
		parentTC:  core.TraceContextFromContext(ctx),
		useLinked: useLinked,
	})
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	// Mirror what the real ext/otel adapter does: publish the new
	// span's identity on ctx as a core.TraceContext so downstream
	// callers (spawnGoAsyncTask reading the create-span identity for
	// task.execute's link) see a non-zero traceparent. Without this
	// step the fake TP would never produce parent identities to link
	// to. The synthesized traceparent only has to be W3C-structurally
	// valid; the parent TraceID portion encodes the call index so each
	// span gets a distinct identity.
	synthTC := core.TraceContext{
		Traceparent: fmt.Sprintf("00-%032d-%016d-01", idx+1, idx+1),
	}
	ctx = core.WithTraceContext(ctx, synthTC)
	return core.WithActiveSpan(ctx, sp), sp
}

func (p *fakeTP) findCall(name string) *recordedCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.calls {
		if p.calls[i].name == name {
			return &p.calls[i]
		}
	}
	return nil
}

func (p *fakeTP) findSpan(name string) *fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sp := range p.spans {
		if sp.name == name {
			return sp
		}
	}
	return nil
}

func (p *fakeTP) lastSpan(name string) *fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	var last *fakeSpan
	for _, sp := range p.spans {
		if sp.name == name {
			last = sp
		}
	}
	return last
}

func (p *fakeTP) spanCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.spans)
}

// --- Server fixture parameterized on tools + completion semantics -----------

type traceTestOpts struct {
	// includes whichever async-eligible tools the test needs:
	//   "fast-task"  — completes immediately, no blocking
	//   "fail-task"  — returns an error (handler path failure)
	//   "panic-task" — panics inside the handler
	//   "slow-task"  — blocks on releaseSignal until released, useful for cancel
	includeSlow bool
	releaseCh   chan struct{}
}

// newTraceServer builds a server identical in shape to newTaskV2Server but
// installs `tp` on BOTH server.WithTracerProvider (so the inbound dispatch
// span exists for v2 method handlers' SpanFromContext) AND
// tasks.Config.TracerProvider (so the goroutine emits task.execute).
func newTraceServer(t *testing.T, tp core.TracerProvider, opts traceTestOpts) *Server {
	t.Helper()
	srv := NewServer(
		core.ServerInfo{Name: "tasks-v2-trace", Version: "0.0.1"},
		WithTracerProvider(tp),
	)

	// Every tool follows the SEP-2663 Option-2 two-phase shape:
	//   - first sync invocation (no TaskContext on ctx) → return
	//     core.GoAsyncResult{} to opt into background continuation.
	//   - goroutine re-invocation (TaskContext present) → do the
	//     actual work and return a sync ToolResult.
	// This is the shape the existing newTaskV2ServerWithSlow fixture
	// uses; we mirror it so each new tool exercises the
	// spawnGoAsyncTask path the span instrumentation lives on.

	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast-task",
			Description: "Async-eligible, completes immediately in the goroutine",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			return core.TextResult("fast-done"), nil
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "fail-task",
			Description: "Async-eligible, handler returns an error in the goroutine",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			return nil, errors.New("intentional handler failure")
		},
	)

	srv.RegisterTool(
		core.ToolDef{
			Name:        "panic-task",
			Description: "Async-eligible, handler panics in the goroutine",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			panic("intentional handler panic")
		},
	)

	// sync-task is the wrapSyncAsCompletedTask path: handler returns a
	// plain ToolResult on the FIRST invocation (no GoAsync sentinel), so
	// the middleware mints a task that's born terminal — no goroutine,
	// no task.execute span. Used by TestTrace_SyncAsCompletedTask_NoExecuteSpan
	// to pin the "work inside the create span doesn't get a separate
	// span" guard.
	srv.RegisterTool(
		core.ToolDef{
			Name:        "sync-task",
			Description: "Async-eligible, completes synchronously (no GoAsync)",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			return core.TextResult("sync-done"), nil
		},
	)

	if opts.includeSlow {
		release := opts.releaseCh
		srv.RegisterTool(
			core.ToolDef{
				Name:        "slow-task",
				Description: "Async-eligible, blocks until released or ctx cancelled",
				InputSchema: map[string]any{"type": "object"},
				Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
			},
			func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
				if tasks.GetTaskContext(ctx) == nil {
					return core.GoAsyncResult{}, nil
				}
				select {
				case <-release:
					return core.TextResult("slow-done"), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		)
	}

	tasks.Register(tasks.Config{Server: srv, TracerProvider: tp, DefaultPollMs: 25})
	return srv
}

func connectTraceClient(t *testing.T, srv *Server) *client.Client {
	t.Helper()
	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "v2-trace", Version: "0.0.1"},
		client.WithTasksExtension())
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })
	return c
}

func mustCreateTask(t *testing.T, c *client.Client, toolName string) string {
	t.Helper()
	res, err := c.Call("tools/call", map[string]any{
		"name":      toolName,
		"arguments": map[string]any{},
	})
	require.NoError(t, err)
	var ctr core.CreateTaskResult
	require.NoError(t, json.Unmarshal(res.Raw, &ctr))
	require.NotEmpty(t, ctr.TaskID)
	return ctr.TaskID
}

// waitForCondition polls cond every 5ms up to 1s. Returns true when cond
// returns true, false on timeout. Used instead of fixed sleeps so tests
// don't fall over on slow CI.
func waitForCondition(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func waitForTaskTerminal(t *testing.T, c *client.Client, taskID string) core.DetailedTask {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		res, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
		require.NoError(t, err)
		var dt core.DetailedTask
		require.NoError(t, json.Unmarshal(res.Raw, &dt))
		if dt.Status.IsTerminal() || dt.Status == core.TaskInputRequired {
			return dt
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s never reached terminal", taskID)
	return core.DetailedTask{}
}

// --- Tests ------------------------------------------------------------------

// TestTrace_GoAsync_Completion: task.execute span is emitted on the GoAsync
// path, via the LinkedTracerProvider path (not plain StartSpan), is marked
// as a NEW root (so the OTel adapter would scrub the inherited parent),
// carries a Link back to the originating create span, and ends with
// mcp.task.status="completed" stamped exactly once.
func TestTrace_GoAsync_Completion(t *testing.T) {
	tp := &fakeTP{}
	srv := newTraceServer(t, tp, traceTestOpts{})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "fast-task")
	waitForTaskTerminal(t, c, taskID)

	// task.execute span emission
	call := tp.findCall("task.execute")
	require.NotNil(t, call, "task.execute span must be emitted on the GoAsync path")
	assert.True(t, call.useLinked, "task.execute MUST come in via StartSpanLinked, not plain StartSpan")
	assert.True(t, call.rootSpan, "bgCtx MUST be marked WithNewRootSpan so the OTel adapter scrubs the inherited parent")
	require.Len(t, call.links, 1, "task.execute MUST carry exactly one Link back to the create span")
	assert.NotEmpty(t, call.links[0].TraceContext.Traceparent, "linked TraceContext must reflect a non-zero create-span traceparent")

	// mcp.task.id attribute set at StartSpan time
	var foundID bool
	for _, a := range call.attrs {
		if a.Key == "mcp.task.id" {
			assert.Equal(t, taskID, a.Value)
			foundID = true
		}
	}
	assert.True(t, foundID, "task.execute span MUST carry mcp.task.id at start")

	// Terminal status + End
	sp := tp.findSpan("task.execute")
	require.NotNil(t, sp)
	require.True(t, waitForCondition(func() bool { return sp.isEnded() }),
		"task.execute span must End once the goroutine terminates")
	assert.Equal(t, string(core.TaskCompleted), sp.attr("mcp.task.status"),
		"mcp.task.status MUST reflect the final terminal status (completed)")
	assert.Equal(t, 1, sp.endTimes(), "End MUST be called exactly once")
	assert.Equal(t, 0, sp.errCount(), "completion path MUST NOT call RecordError")
}

// TestTrace_GoAsync_HandlerError pins v2 semantics for handler-returned
// errors: SEP-2663 treats `(nil, err)` from a tool handler as a
// COMPLETED task whose ToolResult carries IsError=true — NOT a "failed"
// task. The "failed" status (and the matching span RecordError) is
// reserved for protocol-level errors (mwErr, resp.Error, unexpected
// result shape, panic recover). The span still ends cleanly with
// mcp.task.status="completed". The panic / protocol-error coverage
// lives in TestTrace_GoAsync_Panic.
func TestTrace_GoAsync_HandlerError(t *testing.T) {
	tp := &fakeTP{}
	srv := newTraceServer(t, tp, traceTestOpts{})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "fail-task")
	waitForTaskTerminal(t, c, taskID)

	sp := tp.findSpan("task.execute")
	require.NotNil(t, sp)
	require.True(t, waitForCondition(func() bool { return sp.isEnded() }))
	assert.Equal(t, string(core.TaskCompleted), sp.attr("mcp.task.status"),
		"handler-returned errors are v2 'completed' (IsError=true on the inner ToolResult), not 'failed'")
	assert.Equal(t, 0, sp.errCount(),
		"RecordError is reserved for protocol-level failures; handler errors flow through completion")
}

// TestTrace_GoAsync_Panic: handler panics → goroutine recovers, RecordError
// fires for the panic, span Ends with mcp.task.status="failed". The recover
// branch and the goroutine's exit are distinct code paths; both must
// converge on a single End call.
func TestTrace_GoAsync_Panic(t *testing.T) {
	tp := &fakeTP{}
	srv := newTraceServer(t, tp, traceTestOpts{})
	c := connectTraceClient(t, srv)

	// panic-task call may itself surface a synthesized error in the
	// initial response; the goroutine still spawns and emits the span,
	// which is what we're asserting on. Ignore the call's own error.
	res, err := c.Call("tools/call", map[string]any{
		"name":      "panic-task",
		"arguments": map[string]any{},
	})
	require.NoError(t, err)
	var ctr core.CreateTaskResult
	require.NoError(t, json.Unmarshal(res.Raw, &ctr))
	require.NotEmpty(t, ctr.TaskID)
	waitForTaskTerminal(t, c, ctr.TaskID)

	sp := tp.findSpan("task.execute")
	require.NotNil(t, sp)
	require.True(t, waitForCondition(func() bool { return sp.isEnded() }))
	assert.Equal(t, string(core.TaskFailed), sp.attr("mcp.task.status"))
	assert.Equal(t, 1, sp.endTimes(),
		"panic recover + terminal stamp converge on a single End call (defer guards against double-End)")
}

// TestTrace_GoAsync_Cancel: cancelling the task via tasks/cancel transitions
// the store to cancelled, the goroutine's ctx is cancelled, the handler
// returns ctx.Err(), and the span ends with mcp.task.status="cancelled".
func TestTrace_GoAsync_Cancel(t *testing.T) {
	tp := &fakeTP{}
	release := make(chan struct{})
	srv := newTraceServer(t, tp, traceTestOpts{includeSlow: true, releaseCh: release})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "slow-task")

	// Wait for the goroutine to actually start before cancelling — the
	// task.execute span has to exist before we can assert it ended.
	require.True(t, waitForCondition(func() bool {
		return tp.findCall("task.execute") != nil
	}), "task.execute span must be emitted before cancel")

	_, err := c.Call("tasks/cancel", map[string]any{"taskId": taskID})
	require.NoError(t, err)

	sp := tp.findSpan("task.execute")
	require.NotNil(t, sp)
	require.True(t, waitForCondition(func() bool { return sp.isEnded() }),
		"cancelled task.execute must End once the goroutine exits")
	assert.Equal(t, string(core.TaskCancelled), sp.attr("mcp.task.status"))

	// Release the channel so the (already-cancelled) handler's select
	// can return without leaking the goroutine into other tests.
	close(release)
}

// TestTrace_TasksGet_AddsLink: tasks/get dispatch span receives one AddLink
// back to the originating create span so backends can navigate
// poll → lifecycle. With TracerProvider configured on the server, the
// dispatch span is the active span returned by core.SpanFromContext inside
// the handler.
func TestTrace_TasksGet_AddsLink(t *testing.T) {
	tp := &fakeTP{}
	srv := newTraceServer(t, tp, traceTestOpts{})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "fast-task")
	waitForTaskTerminal(t, c, taskID)

	// Snapshot AddLink counts on existing spans before tasks/get so the
	// assertion isolates the new link to the just-issued request.
	preLinkCounts := map[*fakeSpan]int{}
	for _, sp := range tp.spans {
		preLinkCounts[sp] = sp.addLinkCount()
	}

	_, err := c.Call("tasks/get", map[string]any{"taskId": taskID})
	require.NoError(t, err)

	// Find the dispatch span that gained an AddLink.
	var pollSpan *fakeSpan
	for _, sp := range tp.spans {
		if sp.addLinkCount() > preLinkCounts[sp] {
			pollSpan = sp
			break
		}
	}
	require.NotNil(t, pollSpan, "tasks/get dispatch span MUST receive an AddLink back to the create span")
}

// TestTrace_TasksUpdate_AddsLink mirrors the tasks/get AddLink assertion
// for tasks/update. The handler validates inputs against a non-terminal
// task; we don't drive a real input-response flow here because the link
// emission is unconditional on parse success.
func TestTrace_TasksUpdate_AddsLink(t *testing.T) {
	tp := &fakeTP{}
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := newTraceServer(t, tp, traceTestOpts{includeSlow: true, releaseCh: release})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "slow-task")
	require.True(t, waitForCondition(func() bool {
		return tp.findCall("task.execute") != nil
	}), "task.execute span must emit before tasks/update")

	preLinkCounts := map[*fakeSpan]int{}
	for _, sp := range tp.spans {
		preLinkCounts[sp] = sp.addLinkCount()
	}

	// Send tasks/update with no input responses — the AddLink fires before
	// the body is processed. The handler may return an error (task isn't
	// in input_required), but the link emission is what we're asserting.
	_, _ = c.Call("tasks/update", map[string]any{"taskId": taskID, "inputResponses": map[string]any{}})

	var updateSpan *fakeSpan
	for _, sp := range tp.spans {
		if sp.addLinkCount() > preLinkCounts[sp] {
			updateSpan = sp
			break
		}
	}
	require.NotNil(t, updateSpan, "tasks/update dispatch span MUST receive an AddLink back to the create span")
}

// TestTrace_TasksCancel_AddsLink mirrors the AddLink assertion for
// tasks/cancel.
func TestTrace_TasksCancel_AddsLink(t *testing.T) {
	tp := &fakeTP{}
	release := make(chan struct{})
	srv := newTraceServer(t, tp, traceTestOpts{includeSlow: true, releaseCh: release})
	c := connectTraceClient(t, srv)

	taskID := mustCreateTask(t, c, "slow-task")
	require.True(t, waitForCondition(func() bool {
		return tp.findCall("task.execute") != nil
	}))

	preLinkCounts := map[*fakeSpan]int{}
	for _, sp := range tp.spans {
		preLinkCounts[sp] = sp.addLinkCount()
	}

	_, err := c.Call("tasks/cancel", map[string]any{"taskId": taskID})
	require.NoError(t, err)
	close(release)

	var cancelSpan *fakeSpan
	for _, sp := range tp.spans {
		if sp.addLinkCount() > preLinkCounts[sp] {
			cancelSpan = sp
			break
		}
	}
	require.NotNil(t, cancelSpan, "tasks/cancel dispatch span MUST receive an AddLink back to the create span")
}

// TestTrace_SyncAsCompletedTask_NoExecuteSpan: when the handler returns a
// plain ToolResult (no GoAsync sentinel) we still mint a task envelope so
// the wire shape stays uniform, but we do NOT emit task.execute — the
// "work that escapes the request span" framing in issue 659 does not
// apply to the synchronous path. The dispatch span already covers it.
func TestTrace_SyncAsCompletedTask_NoExecuteSpan(t *testing.T) {
	tp := &fakeTP{}
	srv := newTraceServer(t, tp, traceTestOpts{})
	c := connectTraceClient(t, srv)

	// sync-task returns a plain ToolResult on the FIRST handler
	// invocation, so wrapSyncAsCompletedTask is the branch taken — no
	// goroutine spawns, no task.execute span is emitted. The dispatch
	// span already covers the work; emitting an additional zero-
	// duration span would be noise.
	_ = mustCreateTask(t, c, "sync-task")
	time.Sleep(50 * time.Millisecond) // beat for any background work

	assert.Nil(t, tp.findCall("task.execute"),
		"wrapSyncAsCompletedTask path MUST NOT emit task.execute — the work lives inside the create span")
}

// TestTrace_NoopTracerProvider_NoSpansEmitted pins the zero-overhead
// contract: leaving Config.TracerProvider unset (or core.NoopTracerProvider)
// emits no spans, even on the GoAsync path. The existing test suite
// already exercises the unconfigured path; this test makes the contract
// explicit so a future refactor doesn't silently start emitting spans
// against an unconfigured registration.
func TestTrace_NoopTracerProvider_NoSpansEmitted(t *testing.T) {
	srv := NewServer(core.ServerInfo{Name: "tasks-v2-noop", Version: "0.0.1"})
	srv.RegisterTool(
		core.ToolDef{
			Name:        "fast-task",
			Description: "Async-eligible, goes through the GoAsync path",
			InputSchema: map[string]any{"type": "object"},
			Execution:   &core.ToolExecution{TaskSupport: core.TaskSupportOptional},
		},
		func(ctx core.ToolContext, req core.ToolRequest) (core.ToolResponse, error) {
			if tasks.GetTaskContext(ctx) == nil {
				return core.GoAsyncResult{}, nil
			}
			return core.TextResult("fast-done"), nil
		},
	)
	// No TracerProvider on either Server or Config — defaults route to Noop.
	tasks.Register(tasks.Config{Server: srv, DefaultPollMs: 25})

	handler := srv.Handler(WithStreamableHTTP(true))
	ts := httptest.NewServer(handler)
	defer ts.Close()

	c := client.NewClient(ts.URL+"/mcp", core.ClientInfo{Name: "v2-noop", Version: "0.0.1"},
		client.WithTasksExtension())
	require.NoError(t, c.Connect())
	defer c.Close()

	taskID := mustCreateTask(t, c, "fast-task")
	waitForTaskTerminal(t, c, taskID)

	// Nothing to assert about a span here — the contract is "no panic, no
	// allocation, no install." The compile-time path that calls
	// core.StartSpanLinked on core.NoopTracerProvider returns a noopSpan;
	// the assertion is that the call completed without error.
}
