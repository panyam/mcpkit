package skills

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// Client wraps a *client.Client with SEP-2640 host-workflow helpers:
// capability detection, discovery index fetch, manifest and supporting
// file reads, digest verification, and SEP-414 P7 (#748) activation
// telemetry.
//
// The wrapper holds the underlying client pointer plus optional
// telemetry plumbing (TracerProvider + activation hook). Methods are
// safe for concurrent use to the same degree *client.Client is.
//
// Typical host workflow:
//
//	mcp := client.NewClient(serverURL, info)
//	mcp.Connect()
//	sc := skills.NewClient(mcp,
//	    skills.WithTracerProvider(tp),
//	    skills.WithActivationHook(myMetrics.RecordSkillActivation),
//	)
//	if !sc.SupportsSkills() { return }
//	idx, err := sc.ListSkills(ctx)
//	for _, entry := range idx.Skills {
//	    result, err := sc.ReadAndVerify(ctx, entry.URL, entry.Digest)
//	    // result.DigestVerified is true on match; ErrDigestMismatch otherwise
//	}
//	// At the point in the agent loop where the skill enters model context:
//	sc.Activate(ctx, "skill://pdf-processing/SKILL.md",
//	    skills.WithReason("agent_decided"))
type Client struct {
	mcp *client.Client
	cfg clientConfig

	// mu guards consumed, the running total charged against
	// cfg.serverByteBudget. Kept separate from cfg (immutable after
	// construction) because it is the one piece of mutable Client state.
	mu       sync.Mutex
	consumed int64
}

// NewClient builds a SEP-2640 host helper over the given mcpkit client.
// The underlying client must already be Connect()ed for any read method
// to succeed.
//
// Options configure SEP-414 P7 (#748) telemetry plumbing:
//
//   - WithTracerProvider — span emission around read methods + Activate
//   - WithActivationHook — non-OTel telemetry callback fired from Activate
//   - WithMaxResourceBytes — per-resource fetch-size cap (issue 867)
//   - WithServerByteBudget — cumulative fetch-size budget (issue 867)
//
// With no options, the helper behaves identically to the pre-P7 shape
// aside from the per-resource size cap, which defaults to
// DefaultMaxResourceBytes: zero telemetry overhead, no spans, no hook
// calls. NoopTracerProvider is the default — call sites do not nil-check.
func NewClient(mcp *client.Client, opts ...Option) *Client {
	cfg := clientConfig{tp: core.NoopTracerProvider{}, maxResourceBytes: DefaultMaxResourceBytes}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.tp == nil {
		cfg.tp = core.NoopTracerProvider{}
	}
	return &Client{mcp: mcp, cfg: cfg}
}

// SupportsSkills reports whether the connected server advertises the
// io.modelcontextprotocol/skills extension in its initialize (or
// server/discover) response. Hosts iterating connected servers can
// use this to skip ListSkills calls against servers that do not
// support the extension.
//
// The signal is read from the cached initialize/discover response on
// the underlying client; this method does not issue a network call.
func (c *Client) SupportsSkills() bool {
	return c.mcp.ServerSupportsExtension(ExtensionID)
}

// SupportsDirectoryRead reports whether the connected server advertised
// the SEP-2640 directoryRead capability (added by SEP commit 2e04c48d
// on 2026-06-09) inside the io.modelcontextprotocol/skills extension.
// Per the SEP's normative wording, clients MUST NOT call
// resources/directory/read against a server that has not declared
// directoryRead: true. ReadDirectory's pre-call guard uses this check.
//
// The signal is read from the cached initialize/discover response; this
// method does not issue a network call.
func (c *Client) SupportsDirectoryRead() bool {
	cap, ok := c.mcp.ServerExtensionCapability(ExtensionID)
	if !ok {
		return false
	}
	v, _ := cap.Config[CapabilityDirectoryRead].(bool)
	return v
}

// ListSkills reads skill://index.json and returns the parsed Index.
//
// SEP-2640 makes the index OPTIONAL: a server MAY decline to expose
// it. When the read returns a not-found error, ListSkills returns an
// empty Index (with the Schema field unset) and no error so callers
// can treat absent indexes the same as empty ones. Other read errors
// (transport, malformed JSON) propagate.
//
// ctx parents the SEP-414 P7 (#748) `skills.list` span when a
// TracerProvider is installed. On success the span carries
// `mcp.skill.count`. ctx is not threaded to the underlying client
// (the legacy ReadResource API is ctx-free); it only governs span
// parentage.
func (c *Client) ListSkills(ctx context.Context) (Index, error) {
	_, span := c.cfg.tp.StartSpan(ctx, "skills.list",
		core.Attribute{Key: "mcp.skill.uri", Value: IndexURI},
	)
	defer span.End()

	body, err := c.mcp.ReadResource(IndexURI)
	if err != nil {
		if isNotFoundErr(err) {
			span.SetAttribute("mcp.skill.index_absent", "true")
			return Index{}, nil
		}
		span.RecordError(err)
		return Index{}, fmt.Errorf("skills: read %s: %w", IndexURI, err)
	}
	var idx Index
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		span.RecordError(err)
		return Index{}, fmt.Errorf("skills: parse %s: %w", IndexURI, err)
	}
	span.SetAttribute("mcp.skill.count", fmt.Sprintf("%d", len(idx.Skills)))
	return idx, nil
}

// ReadSkillURI reads any skill:// URI and returns the bytes the server
// served. Used when a host receives a skill URI from server
// instructions, the user, or another skill and wants to fetch the
// content without going through the index.
//
// Returns text bytes when the resource is text-typed; base64-decoded
// blob bytes when the resource is binary-typed (e.g., archive entries
// served in archive mode).
//
// ctx parents the SEP-414 P7 (#748) `skills.read` span when a
// TracerProvider is installed. ctx is not threaded to the underlying
// client (the legacy ReadResourceFull API is ctx-free); it only
// governs span parentage.
func (c *Client) ReadSkillURI(ctx context.Context, uri string) ([]byte, error) {
	_, span := c.cfg.tp.StartSpan(ctx, "skills.read",
		core.Attribute{Key: "mcp.skill.uri", Value: uri},
	)
	defer span.End()

	result, err := c.mcp.ReadResourceFull(uri)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("skills: read %s: %w", uri, err)
	}
	bytes, err := c.boundedBytes(result.Contents, uri)
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	return bytes, nil
}

// ReadDirectoryOption tunes a single ReadDirectory call.
type ReadDirectoryOption func(*readDirectoryConfig)

type readDirectoryConfig struct {
	cursor string
}

// WithDirectoryCursor sets the pagination cursor for the call. Pass the
// NextCursor returned by a prior ReadDirectory result to fetch the next
// page. Empty cursor (the default) requests the first page.
func WithDirectoryCursor(cursor string) ReadDirectoryOption {
	return func(c *readDirectoryConfig) { c.cursor = cursor }
}

// ReadDirectory issues a SEP-2640 resources/directory/read call for uri
// and returns the directory's direct children.
//
// Per the SEP (commit 2e04c48d, 2026-06-09): files carry their ordinary
// resource metadata; subdirectories carry MimeTypeDirectory and a URI
// without trailing slash. Clients descend by calling ReadDirectory again
// on a child directory.
//
// Pre-call guard: SupportsDirectoryRead must return true on the
// underlying connection, otherwise ReadDirectory returns
// ErrDirectoryReadNotSupported without issuing a network call. This
// matches the SEP's normative "Clients MUST NOT call" wording.
//
// ctx parents the SEP-414 P7 (#748) `skills.read_directory` span when a
// TracerProvider is installed. On success the span carries
// mcp.skill.uri and mcp.skill.entries (count of returned ResourceDefs).
func (c *Client) ReadDirectory(ctx context.Context, uri string, opts ...ReadDirectoryOption) (DirectoryReadResult, error) {
	var cfg readDirectoryConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	_, span := c.cfg.tp.StartSpan(ctx, "skills.read_directory",
		core.Attribute{Key: "mcp.skill.uri", Value: uri},
	)
	defer span.End()

	if !c.SupportsDirectoryRead() {
		span.RecordError(ErrDirectoryReadNotSupported)
		return DirectoryReadResult{}, ErrDirectoryReadNotSupported
	}

	params := DirectoryReadRequest{URI: uri, Cursor: cfg.cursor}
	result, err := c.mcp.Call(MethodResourcesDirectoryRead, params)
	if err != nil {
		span.RecordError(err)
		return DirectoryReadResult{}, fmt.Errorf("skills: %s %s: %w", MethodResourcesDirectoryRead, uri, err)
	}
	var out DirectoryReadResult
	if err := result.Unmarshal(&out); err != nil {
		span.RecordError(err)
		return DirectoryReadResult{}, fmt.Errorf("skills: decode %s response: %w", MethodResourcesDirectoryRead, err)
	}
	span.SetAttribute("mcp.skill.entries", fmt.Sprintf("%d", len(out.Resources)))
	return out, nil
}

// SkillManifest holds a parsed SKILL.md plus the raw bytes the server
// served. Raw is preserved so the caller can verify against a digest
// after parsing — the digest is computed over the raw artifact, not the
// post-parse representation.
type SkillManifest struct {
	URI         string
	Frontmatter Frontmatter
	Body        []byte
	Raw         []byte
}

// ReadSkillManifest fetches a SKILL.md URI, parses the YAML frontmatter,
// and returns both the typed Frontmatter and the raw + post-frontmatter
// body bytes.
//
// Validates that uri is a manifest URI (ends in /SKILL.md). For non-
// manifest URIs use ReadSkillURI.
//
// ctx parents the SEP-414 P7 (#748) `skills.read_manifest` span when a
// TracerProvider is installed. On success the span carries
// mcp.skill.path and mcp.skill.name in addition to mcp.skill.uri.
func (c *Client) ReadSkillManifest(ctx context.Context, uri string) (*SkillManifest, error) {
	ctx, span := c.cfg.tp.StartSpan(ctx, "skills.read_manifest",
		core.Attribute{Key: "mcp.skill.uri", Value: uri},
	)
	defer span.End()

	parts, err := ParseURI(uri)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("skills: %s: %w", uri, err)
	}
	if !parts.IsManifest {
		err := fmt.Errorf("%w: %q", ErrNotManifestURI, uri)
		span.RecordError(err)
		return nil, err
	}
	if path := strings.Join(parts.SkillPath, "/"); path != "" {
		span.SetAttribute("mcp.skill.path", path)
	}
	raw, err := c.ReadSkillURI(ctx, uri)
	if err != nil {
		// inner span on c.ReadSkillURI already recorded the error
		return nil, err
	}
	fm, body, err := ParseFrontmatter(raw)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("skills: parse frontmatter for %s: %w", uri, err)
	}
	if fm.Name != "" {
		span.SetAttribute("mcp.skill.name", fm.Name)
	}
	return &SkillManifest{
		URI:         uri,
		Frontmatter: fm,
		Body:        body,
		Raw:         raw,
	}, nil
}

// ReadSkillFile resolves a relative reference against a manifest's
// skill root and reads the result via skill://. Used to follow links
// inside a SKILL.md body (e.g., "references/GUIDE.md") per SEP-2640's
// filesystem-style resolution.
//
// Returns the same byte semantics as ReadSkillURI. The inner
// ReadSkillURI call emits the SEP-414 P7 (#748) `skills.read` span;
// ReadSkillFile itself adds no wrapping span (it's a thin URI
// resolver + delegated read).
func (c *Client) ReadSkillFile(ctx context.Context, manifest *SkillManifest, relPath string) ([]byte, error) {
	if manifest == nil {
		return nil, fmt.Errorf("skills: ReadSkillFile: nil manifest")
	}
	root, err := ParseURI(manifest.URI)
	if err != nil {
		return nil, fmt.Errorf("skills: ReadSkillFile: re-parse manifest URI: %w", err)
	}
	resolved, err := ResolveRelative(root, relPath)
	if err != nil {
		return nil, fmt.Errorf("skills: resolve %q against %s: %w", relPath, manifest.URI, err)
	}
	return c.ReadSkillURI(ctx, resolved.String())
}

// ReadSkillFileVerified resolves relPath against the manifest's skill
// root, reads it, and verifies the served bytes against the per-file
// digest pinned in the entry's supporting-file pins (issue 866). It is the integrity-checked
// counterpart to ReadSkillFile: use it for supporting files a re-
// verifying host must not trust unverified (scripts, templates,
// referenced docs), so a swapped file is rejected on read.
//
// entry is the IndexEntry for the skill (from ListSkills + Index.Lookup),
// which carries the supporting-file pins mcpkit's Indexer writes under
// MetaKeyFileDigests (read via entry.FileDigest). The lookup key is the
// file's path relative to the skill root, derived from the resolved URI
// so that "./x", "a/../b", and percent-encoded forms all match the
// canonical pin.
//
// Errors:
//   - ErrSupportingFileUnpinned when the entry carries no pin for the
//     resolved path (secure default — no silent unverified fallback).
//   - ErrDigestMismatch when the served bytes do not match the pin; per
//     SEP-2640 the host MUST NOT use the bytes.
//
// On success returns a ReadResult with DigestVerified=true.
func (c *Client) ReadSkillFileVerified(ctx context.Context, entry IndexEntry, manifest *SkillManifest, relPath string) (*ReadResult, error) {
	if manifest == nil {
		return nil, fmt.Errorf("skills: ReadSkillFileVerified: nil manifest")
	}
	root, err := ParseURI(manifest.URI)
	if err != nil {
		return nil, fmt.Errorf("skills: ReadSkillFileVerified: re-parse manifest URI: %w", err)
	}
	resolved, err := ResolveRelative(root, relPath)
	if err != nil {
		return nil, fmt.Errorf("skills: resolve %q against %s: %w", relPath, manifest.URI, err)
	}
	key := skillRelPath(root, resolved)
	digest, ok := entry.FileDigest(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrSupportingFileUnpinned, key)
	}
	return c.ReadAndVerify(ctx, resolved.String(), digest)
}

// skillRelPath returns the resolved file's path relative to the skill
// root — the canonical key the supporting-file pins use. ResolveRelative
// guarantees resolved.AllSegments is prefixed by root.SkillPath and lands
// strictly below the skill directory, so the suffix is the in-skill
// relative path.
func skillRelPath(root, resolved URIParts) string {
	if len(resolved.AllSegments) <= len(root.SkillPath) {
		return ""
	}
	return strings.Join(resolved.AllSegments[len(root.SkillPath):], "/")
}

// ReadResult is the typed return from ReadAndVerify. Bytes carries the
// served content; DigestVerified is true when the SHA-256 over Bytes
// equals the expected digest the caller supplied.
type ReadResult struct {
	URI            string
	Bytes          []byte
	DigestVerified bool
}

// ReadAndVerify reads uri and checks the SHA-256 of the served bytes
// against expectedDigest (in SEP-2640's "sha256:{64-hex}" format).
//
// On match, returns a ReadResult with DigestVerified=true and no
// error. On mismatch, returns ErrDigestMismatch wrapped with the
// expected and actual digests for diagnostics — per SEP-2640 the host
// MUST NOT use the bytes in that case.
//
// expectedDigest of "" disables verification: ReadAndVerify behaves
// like ReadSkillURI and returns DigestVerified=false. This is a
// convenience for hosts that have a URI but no catalogued digest
// (server instructions, user-supplied URIs).
//
// ctx parents the SEP-414 P7 (#748) `skills.read_and_verify` span when
// a TracerProvider is installed. The span carries mcp.skill.uri,
// mcp.skill.expected_digest, and (on a non-error return)
// mcp.skill.digest_verified.
func (c *Client) ReadAndVerify(ctx context.Context, uri, expectedDigest string) (*ReadResult, error) {
	ctx, span := c.cfg.tp.StartSpan(ctx, "skills.read_and_verify",
		core.Attribute{Key: "mcp.skill.uri", Value: uri},
		core.Attribute{Key: "mcp.skill.expected_digest", Value: expectedDigest},
	)
	defer span.End()

	body, err := c.ReadSkillURI(ctx, uri)
	if err != nil {
		// inner span on ReadSkillURI already recorded the error
		return nil, err
	}
	result := &ReadResult{URI: uri, Bytes: body}
	if expectedDigest == "" {
		span.SetAttribute("mcp.skill.digest_verified", "false")
		return result, nil
	}
	sum := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, expectedDigest) {
		mismatchErr := fmt.Errorf("%w: want %s, got %s", ErrDigestMismatch, expectedDigest, got)
		span.RecordError(mismatchErr)
		return nil, mismatchErr
	}
	span.SetAttribute("mcp.skill.digest_verified", "true")
	result.DigestVerified = true
	return result, nil
}

// Activate signals SEP-414 P7 (#748) that the host just put the skill
// at uri into the model context. It is a PURE SDK-side telemetry emit
// point — no MCP wire traffic, no spec contract. The activation may
// follow a cached manifest read, a freshly-loaded skill, or any other
// path the agent loop chose; mcpkit's wire surface cannot otherwise
// observe the use because cached skills activate invisibly.
//
// Side effects:
//
//   - When a TracerProvider is installed, emits an instant
//     `skills.activate` span (Start+End back-to-back — activation is
//     point-in-time, not a duration). Span attributes:
//     mcp.skill.uri, mcp.skill.path (when uri is a manifest URI),
//     mcp.skill.activation.reason (when WithReason is supplied).
//   - When an activation hook is installed (WithActivationHook), calls
//     it synchronously with the same ActivationEvent that is returned.
//
// The returned ActivationEvent is a structured pull-style emit
// duplicate of what the span / hook saw, suitable for non-OTel
// telemetry the caller wants to feed independently. Both telemetry
// sinks see the same Timestamp.
//
// uri is not validated — Activate accepts any string the host wants to
// report (manifest URI, sub-file URI, even an opaque identifier the
// host minted for an in-process bundle). When uri parses as a SEP-2640
// manifest URI, mcp.skill.path is emitted as well.
//
// ctx parents the span. A canceled ctx does not block the activation —
// activation telemetry MUST NOT depend on context liveness because the
// agent loop reports activations after the originating handler ctx may
// have ended.
func (c *Client) Activate(ctx context.Context, uri string, opts ...ActivateOption) ActivationEvent {
	cfg := activateConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	event := ActivationEvent{
		URI:       uri,
		Reason:    cfg.reason,
		Timestamp: time.Now(),
	}

	attrs := []core.Attribute{
		{Key: "mcp.skill.uri", Value: uri},
	}
	if parts, err := ParseURI(uri); err == nil && len(parts.SkillPath) > 0 {
		attrs = append(attrs, core.Attribute{
			Key:   "mcp.skill.path",
			Value: strings.Join(parts.SkillPath, "/"),
		})
	}
	if cfg.reason != "" {
		attrs = append(attrs, core.Attribute{
			Key:   "mcp.skill.activation.reason",
			Value: cfg.reason,
		})
	}

	_, span := c.cfg.tp.StartSpan(ctx, "skills.activate", attrs...)
	span.End()

	if c.cfg.activationHook != nil {
		c.cfg.activationHook(ctx, event)
	}
	return event
}

// ReadResourceTool surfaces the SEP-2640 Implementation Guidelines
// host-exposed tool schema. Hosts that want to expose a generic
// resource-reading tool to their LLM can register this without
// copy-pasting the JSON Schema literal from the spec.
//
// The shape matches SEP-2640 verbatim:
//
//	{
//	  "name": "read_resource",
//	  "description": "Read an MCP resource from a connected server.",
//	  "inputSchema": {
//	    "type": "object",
//	    "properties": {
//	      "server": { "type": "string", "description": "Name of the connected MCP server" },
//	      "uri":    { "type": "string", "description": "The resource URI" }
//	    },
//	    "required": ["server", "uri"]
//	  }
//	}
const ReadResourceToolName = "read_resource"

// ReadResourceToolDescription is the description string SEP-2640
// suggests for the tool, useful as the model-facing summary.
const ReadResourceToolDescription = "Read an MCP resource from a connected server."

// ReadResourceToolInputSchema is the JSON Schema for the tool's
// arguments per the SEP-2640 sketch. Exposed as a map[string]any so
// hosts can register it directly through mcpkit's tool registration
// path without an intermediate JSON round-trip.
var ReadResourceToolInputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"server": map[string]any{
			"type":        "string",
			"description": "Name of the connected MCP server",
		},
		"uri": map[string]any{
			"type":        "string",
			"description": "The resource URI",
		},
	},
	"required": []string{"server", "uri"},
}

// --- internal helpers -------------------------------------------------------

// isNotFoundErr classifies the wire-level signals mcpkit's resources/read
// returns when the target URI is absent. SEP-2640 says hosts MUST
// tolerate an absent skill://index.json; this helper centralizes the
// matching so the public surface stays simple. Match shape:
//
//   - "not found" — generic JSON-RPC error text from older servers
//   - "unknown resource" — mcpkit's current resources/read shape on
//     resources that were not registered
//
// Both surface as JSON-RPC -32602 in practice; the message substring
// is the stable signal because transports vary in how they expose the
// numeric code.
func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "unknown resource")
}

// BytesConsumed reports the running total charged against the Client's
// WithServerByteBudget across every skill:// read so far. Returns 0 when
// no budget is configured or nothing has been read yet. Safe for
// concurrent use.
func (c *Client) BytesConsumed() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consumed
}

// boundedBytes extracts the served bytes from a resources/read result
// while enforcing the per-resource size cap (WithMaxResourceBytes) and
// the cumulative server byte budget (WithServerByteBudget).
//
// The per-resource size is bounded before a blob is base64-decoded: the
// decoded length is at most DecodedLen(len(base64)) for a blob and the
// raw string length for text, so an oversized payload is rejected
// without incurring the decode (or a subsequent hash) allocation. On
// success the actual decoded byte count is charged against the
// cumulative budget.
func (c *Client) boundedBytes(contents []core.ResourceReadContent, uri string) ([]byte, error) {
	if len(contents) == 0 {
		return nil, fmt.Errorf("skills: %s: empty contents", uri)
	}
	ct := contents[0]

	var estimate int64
	if ct.Blob != "" {
		estimate = int64(base64.StdEncoding.DecodedLen(len(ct.Blob)))
	} else {
		estimate = int64(len(ct.Text))
	}
	if c.cfg.maxResourceBytes > 0 && estimate > c.cfg.maxResourceBytes {
		return nil, fmt.Errorf("%w: %s is ~%d bytes, cap %d",
			ErrResourceTooLarge, uri, estimate, c.cfg.maxResourceBytes)
	}

	bytes, err := extractBytes(contents, uri)
	if err != nil {
		return nil, err
	}

	if c.cfg.serverByteBudget > 0 {
		c.mu.Lock()
		total := c.consumed + int64(len(bytes))
		if total > c.cfg.serverByteBudget {
			c.mu.Unlock()
			return nil, fmt.Errorf("%w: %s would push total to %d, budget %d",
				ErrServerByteBudgetExceeded, uri, total, c.cfg.serverByteBudget)
		}
		c.consumed = total
		c.mu.Unlock()
	}
	return bytes, nil
}

// extractBytes pulls the served bytes out of a ResourceResult's
// Contents slice. Per the wire shape, each entry has either Text (for
// text-typed resources) or Blob (base64-encoded for binary). For the
// skill: scheme today, SKILL.md and supporting files are text;
// archive resources are binary. Returns the first content entry's
// bytes since SEP-2640 resources never carry multi-content responses.
func extractBytes(contents []core.ResourceReadContent, uri string) ([]byte, error) {
	if len(contents) == 0 {
		return nil, fmt.Errorf("skills: %s: empty contents", uri)
	}
	c := contents[0]
	if c.Blob != "" {
		decoded, err := base64.StdEncoding.DecodeString(c.Blob)
		if err != nil {
			return nil, fmt.Errorf("skills: %s: blob base64 decode: %w", uri, err)
		}
		return decoded, nil
	}
	return []byte(c.Text), nil
}

// _ json is referenced indirectly via the ParseFrontmatter call chain.
// Keep the import alive for callers that re-decode the JSON-RPC envelope
// directly (none today, but mcpkit's wire surface is split across
// versions).
var _ = json.Marshal
