package skills

import (
	"context"
	"time"

	"github.com/panyam/mcpkit/core"
)

// Option configures a Client built via NewClient.
//
// SEP-414 P7 (issue 748) introduces tracer + activation-hook plumbing on
// the SEP-2640 host helper. Options follow the same shape as the
// Provider options in this package: pass to NewClient, evaluated once
// at construction, immutable after.
type Option func(*clientConfig)

type clientConfig struct {
	tp             core.TracerProvider
	activationHook func(context.Context, ActivationEvent)

	// maxResourceBytes caps the size of any single skill:// read. 0
	// leaves the DefaultMaxResourceBytes default in place; a negative
	// value disables the cap. See WithMaxResourceBytes.
	maxResourceBytes int64

	// serverByteBudget caps the cumulative bytes a Client fetches across
	// all skill:// reads. 0 (the default) disables the budget. See
	// WithServerByteBudget.
	serverByteBudget int64
}

// DefaultMaxResourceBytes is the per-resource size cap the Client applies
// to skill:// reads when no WithMaxResourceBytes option is supplied.
// 10 MiB comfortably fits a SKILL.md and ordinary supporting files while
// rejecting payloads engineered to exhaust memory at fetch time (WG
// threat model T6, issue 867). It mirrors the archive extractor's own
// DefaultArchiveMaxBytes cap, applied to the individual-file paths the
// archive cap does not cover. Pass -1 to WithMaxResourceBytes to disable.
const DefaultMaxResourceBytes int64 = 10 * 1024 * 1024

// WithMaxResourceBytes sets the per-resource size cap for skill:// reads
// (ReadSkillURI and everything that flows through it: ReadSkillManifest,
// ReadSkillFile, ReadAndVerify). A read whose payload exceeds n is
// rejected with ErrResourceTooLarge before a blob is base64-decoded, so
// an oversized payload never incurs the decode or a subsequent hash
// allocation.
//
// n <= 0 disables the cap (n == 0 is treated as "use the default" at
// construction; pass -1 to explicitly remove the cap). Default is
// DefaultMaxResourceBytes (10 MiB).
//
// Note: the underlying resources/read call still buffers the full
// JSON-RPC response in memory before this cap is applied — bounding that
// requires a streaming read on the core client. This option bounds the
// decode + hash amplification and the retained bytes, which is the SDK
// surface ext/skills controls.
func WithMaxResourceBytes(n int64) Option {
	return func(c *clientConfig) { c.maxResourceBytes = n }
}

// WithServerByteBudget sets a cumulative cap on the total bytes a Client
// fetches across every skill:// read for its lifetime. Once the running
// total plus the next read would exceed n, that read is rejected with
// ErrServerByteBudgetExceeded. This bounds a directory walk that fetches
// many individually-small supporting files past an aggregate ceiling,
// which the per-resource cap alone does not catch.
//
// n <= 0 disables the budget (the default). Reads are charged their
// decoded byte count; the running total is available via BytesConsumed.
func WithServerByteBudget(n int64) Option {
	return func(c *clientConfig) { c.serverByteBudget = n }
}

// WithTracerProvider opts the Client into SEP-414 P7 (#748) span
// emission around its read-path methods + Activate.
//
// When set to a non-Noop provider, each read method emits a span:
//
//   - ListSkills      → skills.list
//   - ReadSkillURI    → skills.read           (attr: mcp.skill.uri)
//   - ReadSkillManifest → skills.read_manifest (attrs: mcp.skill.uri,
//     mcp.skill.path, mcp.skill.name on success)
//   - ReadAndVerify   → skills.read_and_verify (attrs: mcp.skill.uri,
//     mcp.skill.expected_digest, mcp.skill.digest_verified on success)
//   - Activate        → skills.activate        (instant span — Start +
//     End back-to-back; attrs: mcp.skill.uri, mcp.skill.path,
//     mcp.skill.activation.reason when WithReason is set)
//
// Spans inherit the W3C traceparent on the supplied ctx via the
// existing trace context propagation (#644 / #649 / #652), so the
// server-side resources/read dispatch span (now skill-attribute
// enriched per #748 Layer 1) lands as a child of the client read span
// automatically.
//
// nil and core.NoopTracerProvider{} both short-circuit to zero
// overhead — the Client stores NoopTracerProvider as its default so
// call sites do not nil-check.
func WithTracerProvider(tp core.TracerProvider) Option {
	return func(c *clientConfig) {
		if tp == nil {
			tp = core.NoopTracerProvider{}
		}
		c.tp = tp
	}
}

// WithActivationHook installs a callback invoked from Client.Activate
// in addition to the OTel span. The hook fires synchronously inside
// Activate before it returns.
//
// Hosts that do not use OpenTelemetry can install the hook to feed
// their own telemetry pipeline (structured logging, internal counters,
// alternate tracing) without configuring a TracerProvider. The two
// telemetry sinks are independent — installing both fires both.
//
// fn==nil is a no-op (equivalent to omitting the option).
func WithActivationHook(fn func(context.Context, ActivationEvent)) Option {
	return func(c *clientConfig) {
		c.activationHook = fn
	}
}

// ActivationEvent is the payload Client.Activate returns and passes to
// the WithActivationHook callback. It captures the activation moment
// the agent loop signaled — the URI of the skill the host is about to
// put into the model context, an optional human-readable reason, and
// the activation timestamp.
//
// Returned to the caller so non-OTel telemetry pipelines that prefer a
// pull-style emit point can stamp the event in whatever shape they
// want. The same struct is delivered to any installed activation hook,
// so a host with both span emission and a hook installed sees a
// consistent record across both sinks.
type ActivationEvent struct {
	// URI is the skill:// URI the host activated. May be a manifest URI
	// (skill://path/SKILL.md) or a sub-file URI within a skill — the
	// host decides which level of granularity to report.
	URI string

	// Reason is an optional human-readable explanation for the
	// activation, populated when the caller passed WithReason.
	// Surfaced as the mcp.skill.activation.reason span attribute when a
	// TracerProvider is installed.
	Reason string

	// Timestamp is when Activate was called. Hosts can use this to
	// correlate activations against external event logs.
	Timestamp time.Time
}

// ActivateOption tunes a single Client.Activate call.
type ActivateOption func(*activateConfig)

type activateConfig struct {
	reason string
}

// WithReason annotates the activation with a short human-readable
// reason (e.g., "agent_decided_pdf_processing", "user_requested",
// "test").
//
// Surfaced as the mcp.skill.activation.reason span attribute (when a
// TracerProvider is installed) and as the Reason field on the
// returned ActivationEvent. Stays out of band for non-OTel hosts that
// rely solely on the WithActivationHook callback.
func WithReason(reason string) ActivateOption {
	return func(c *activateConfig) {
		c.reason = reason
	}
}
