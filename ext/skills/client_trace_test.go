package skills_test

// SEP-414 P7 (#748) — span emission + activation-hook tests for the
// SEP-2640 host helper. Uses a fake TracerProvider that records every
// span it starts so the suite can run without depending on
// go.opentelemetry.io/otel (mirrors server/trace_middleware_test.go's
// pattern).
//
// Black-box (`package skills_test`) — the production Client + options
// must be reachable through the exported surface alone.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
)

type fakeSpan struct {
	mu    sync.Mutex
	name  string
	attrs map[string]string
	errs  []error
	ended bool
}

func (s *fakeSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *fakeSpan) SetAttribute(k, v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]string)
	}
	s.attrs[k] = v
}

func (s *fakeSpan) RecordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *fakeSpan) AddLink(_ core.Link) {}

func (s *fakeSpan) attr(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[k]
}

type fakeTracerProvider struct {
	mu    sync.Mutex
	spans []*fakeSpan
}

func (p *fakeTracerProvider) StartSpan(ctx context.Context, name string, attrs ...core.Attribute) (context.Context, core.Span) {
	sp := &fakeSpan{name: name, attrs: make(map[string]string, len(attrs))}
	for _, a := range attrs {
		sp.attrs[a.Key] = a.Value
	}
	p.mu.Lock()
	p.spans = append(p.spans, sp)
	p.mu.Unlock()
	return ctx, sp
}

func (p *fakeTracerProvider) snapshot() []*fakeSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*fakeSpan, len(p.spans))
	copy(out, p.spans)
	return out
}

func (p *fakeTracerProvider) byName(name string) []*fakeSpan {
	var out []*fakeSpan
	for _, s := range p.snapshot() {
		if s.name == name {
			out = append(out, s)
		}
	}
	return out
}

// TestClient_ListSkills_EmitsSpan_WithCount is the SEP-414 P7 happy
// path: ListSkills under a TracerProvider emits a `skills.list` span
// carrying mcp.skill.uri (the index URI) and mcp.skill.count on
// success.
func TestClient_ListSkills_EmitsSpan_WithCount(t *testing.T) {
	tp := &fakeTracerProvider{}
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithTracerProvider(tp))

	_, err := sc.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}

	spans := tp.byName("skills.list")
	if len(spans) != 1 {
		t.Fatalf("skills.list span count = %d, want 1", len(spans))
	}
	if got := spans[0].attr("mcp.skill.uri"); got != skills.IndexURI {
		t.Errorf("mcp.skill.uri = %q, want %q", got, skills.IndexURI)
	}
	if got := spans[0].attr("mcp.skill.count"); got == "" || got == "0" {
		t.Errorf("mcp.skill.count = %q, want a non-zero count", got)
	}
	if !spans[0].ended {
		t.Errorf("span not ended")
	}
}

// TestClient_ReadSkillURI_EmitsSpan_WithURI pins that ReadSkillURI
// emits skills.read with mcp.skill.uri.
func TestClient_ReadSkillURI_EmitsSpan_WithURI(t *testing.T) {
	tp := &fakeTracerProvider{}
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithTracerProvider(tp))

	const uri = "skill://git-workflow/SKILL.md"
	if _, err := sc.ReadSkillURI(context.Background(), uri); err != nil {
		t.Fatalf("ReadSkillURI: %v", err)
	}

	spans := tp.byName("skills.read")
	if len(spans) != 1 {
		t.Fatalf("skills.read span count = %d, want 1", len(spans))
	}
	if got := spans[0].attr("mcp.skill.uri"); got != uri {
		t.Errorf("mcp.skill.uri = %q, want %q", got, uri)
	}
}

// TestClient_ReadSkillManifest_EmitsPathAndName verifies the manifest
// read wraps with skills.read_manifest and stamps mcp.skill.path +
// mcp.skill.name on success.
func TestClient_ReadSkillManifest_EmitsPathAndName(t *testing.T) {
	tp := &fakeTracerProvider{}
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithTracerProvider(tp))

	const uri = "skill://pdf-processing/SKILL.md"
	if _, err := sc.ReadSkillManifest(context.Background(), uri); err != nil {
		t.Fatalf("ReadSkillManifest: %v", err)
	}

	spans := tp.byName("skills.read_manifest")
	if len(spans) != 1 {
		t.Fatalf("skills.read_manifest span count = %d, want 1", len(spans))
	}
	if got := spans[0].attr("mcp.skill.uri"); got != uri {
		t.Errorf("mcp.skill.uri = %q, want %q", got, uri)
	}
	if got := spans[0].attr("mcp.skill.path"); got != "pdf-processing" {
		t.Errorf("mcp.skill.path = %q, want %q", got, "pdf-processing")
	}
	if got := spans[0].attr("mcp.skill.name"); got != "pdf-processing" {
		t.Errorf("mcp.skill.name = %q, want %q", got, "pdf-processing")
	}
}

// TestClient_ReadAndVerify_EmitsDigestVerified verifies the
// skills.read_and_verify span carries both the expected digest and the
// digest_verified outcome.
func TestClient_ReadAndVerify_EmitsDigestVerified(t *testing.T) {
	tp := &fakeTracerProvider{}
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithTracerProvider(tp))

	idx, err := sc.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	entry, ok := idx.Lookup("skill://git-workflow/SKILL.md")
	if !ok {
		t.Fatal("git-workflow not in index")
	}

	if _, err := sc.ReadAndVerify(context.Background(), entry.URL, entry.Digest); err != nil {
		t.Fatalf("ReadAndVerify: %v", err)
	}

	spans := tp.byName("skills.read_and_verify")
	if len(spans) != 1 {
		t.Fatalf("skills.read_and_verify span count = %d, want 1", len(spans))
	}
	if got := spans[0].attr("mcp.skill.uri"); got != entry.URL {
		t.Errorf("mcp.skill.uri = %q, want %q", got, entry.URL)
	}
	if got := spans[0].attr("mcp.skill.expected_digest"); got != entry.Digest {
		t.Errorf("mcp.skill.expected_digest = %q, want %q", got, entry.Digest)
	}
	if got := spans[0].attr("mcp.skill.digest_verified"); got != "true" {
		t.Errorf("mcp.skill.digest_verified = %q, want %q", got, "true")
	}
}

// TestClient_Activate_FiresHookAndSpan is the marquee SEP-414 P7 test:
// Activate emits skills.activate, populates skill-shape attributes,
// invokes the WithActivationHook callback with a matching
// ActivationEvent, and returns the same event to the caller.
func TestClient_Activate_FiresHookAndSpan(t *testing.T) {
	tp := &fakeTracerProvider{}
	var captured []skills.ActivationEvent
	var mu sync.Mutex
	hook := func(_ context.Context, ev skills.ActivationEvent) {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, ev)
	}

	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithTracerProvider(tp),
		skills.WithActivationHook(hook),
	)

	const uri = "skill://pdf-processing/SKILL.md"
	const reason = "agent_decided_pdf_for_invoice"
	ev := sc.Activate(context.Background(), uri, skills.WithReason(reason))

	// Returned event shape
	if ev.URI != uri {
		t.Errorf("ev.URI = %q, want %q", ev.URI, uri)
	}
	if ev.Reason != reason {
		t.Errorf("ev.Reason = %q, want %q", ev.Reason, reason)
	}
	if ev.Timestamp.IsZero() {
		t.Errorf("ev.Timestamp is zero")
	}
	if time.Since(ev.Timestamp) > time.Minute {
		t.Errorf("ev.Timestamp = %v, want recent", ev.Timestamp)
	}

	// Hook fired with the same event
	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(captured))
	}
	if captured[0].URI != ev.URI || captured[0].Reason != ev.Reason ||
		!captured[0].Timestamp.Equal(ev.Timestamp) {
		t.Errorf("hook event %+v, want %+v", captured[0], ev)
	}

	// Span emitted with correct shape
	spans := tp.byName("skills.activate")
	if len(spans) != 1 {
		t.Fatalf("skills.activate span count = %d, want 1", len(spans))
	}
	if got := spans[0].attr("mcp.skill.uri"); got != uri {
		t.Errorf("mcp.skill.uri = %q, want %q", got, uri)
	}
	if got := spans[0].attr("mcp.skill.path"); got != "pdf-processing" {
		t.Errorf("mcp.skill.path = %q, want %q", got, "pdf-processing")
	}
	if got := spans[0].attr("mcp.skill.activation.reason"); got != reason {
		t.Errorf("mcp.skill.activation.reason = %q, want %q", got, reason)
	}
	if !spans[0].ended {
		t.Errorf("skills.activate span not ended (Activate is instant — Start+End must both fire)")
	}
}

// TestClient_Activate_NoReason_OmitsReasonAttr pins that
// WithReason is optional — when absent, the attr is NOT emitted (vs.
// being emitted as an empty string).
func TestClient_Activate_NoReason_OmitsReasonAttr(t *testing.T) {
	tp := &fakeTracerProvider{}
	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid", skills.WithTracerProvider(tp))

	_ = sc.Activate(context.Background(), "skill://pdf-processing/SKILL.md")

	spans := tp.byName("skills.activate")
	if len(spans) != 1 {
		t.Fatalf("skills.activate span count = %d, want 1", len(spans))
	}
	if got, ok := spans[0].attrs["mcp.skill.activation.reason"]; ok {
		t.Errorf("mcp.skill.activation.reason present (%q) when WithReason was not supplied", got)
	}
}

// TestClient_Activate_NoTracer_StillFiresHook_NoSpan pins that the
// hook path is independent of the tracer path — a host that wants
// activation telemetry without OTel can install WithActivationHook
// alone and still see every activation.
func TestClient_Activate_NoTracer_StillFiresHook_NoSpan(t *testing.T) {
	var captured []skills.ActivationEvent
	hook := func(_ context.Context, ev skills.ActivationEvent) {
		captured = append(captured, ev)
	}

	sc, _ := connectSkillsClientWithClientOpts(t, "testdata/valid",
		skills.WithActivationHook(hook))

	const uri = "skill://git-workflow/SKILL.md"
	_ = sc.Activate(context.Background(), uri)

	if len(captured) != 1 {
		t.Fatalf("hook calls = %d, want 1", len(captured))
	}
	if captured[0].URI != uri {
		t.Errorf("captured URI = %q, want %q", captured[0].URI, uri)
	}
}

// TestClient_NoTracer_EmitsNoSpans pins the zero-overhead path: a
// Client built without WithTracerProvider (or with nil /
// NoopTracerProvider) does not produce any span observable to the
// adapter — the read methods complete normally but the noop tracer
// records nothing the fakeTracerProvider would see.
func TestClient_NoTracer_EmitsNoSpans(t *testing.T) {
	tp := &fakeTracerProvider{}
	// Deliberately NOT passing WithTracerProvider.
	sc, _ := connectSkillsClient(t, "testdata/valid")

	if _, err := sc.ListSkills(context.Background()); err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if _, err := sc.ReadSkillURI(context.Background(), "skill://git-workflow/SKILL.md"); err != nil {
		t.Fatalf("ReadSkillURI: %v", err)
	}

	if got := len(tp.snapshot()); got != 0 {
		t.Errorf("fake tracer saw %d spans, want 0 (Client default is NoopTracerProvider)", got)
	}
}
