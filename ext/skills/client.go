package skills

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// Client wraps a *client.Client with SEP-2640 host-workflow helpers:
// capability detection, discovery index fetch, manifest and supporting
// file reads, digest verification.
//
// The wrapper holds no state of its own beyond the underlying client
// pointer. Methods are safe for concurrent use to the same degree
// *client.Client is.
//
// Typical host workflow:
//
//	mcp := client.NewClient(serverURL, info)
//	mcp.Connect()
//	sc := skills.NewClient(mcp)
//	if !sc.SupportsSkills() { return }
//	idx, err := sc.ListSkills(ctx)
//	for _, entry := range idx.Skills {
//	    result, err := sc.ReadAndVerify(ctx, entry.URL, entry.Digest)
//	    // result.DigestVerified is true on match; ErrDigestMismatch otherwise
//	}
type Client struct {
	mcp *client.Client
}

// NewClient builds a SEP-2640 host helper over the given mcpkit client.
// The underlying client must already be Connect()ed for any read method
// to succeed.
func NewClient(mcp *client.Client) *Client {
	return &Client{mcp: mcp}
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

// ListSkills reads skill://index.json and returns the parsed Index.
//
// SEP-2640 makes the index OPTIONAL: a server MAY decline to expose
// it. When the read returns a not-found error, ListSkills returns an
// empty Index (with the Schema field unset) and no error so callers
// can treat absent indexes the same as empty ones. Other read errors
// (transport, malformed JSON) propagate.
func (c *Client) ListSkills() (Index, error) {
	body, err := c.mcp.ReadResource(IndexURI)
	if err != nil {
		if isNotFoundErr(err) {
			return Index{}, nil
		}
		return Index{}, fmt.Errorf("skills: read %s: %w", IndexURI, err)
	}
	var idx Index
	if err := json.Unmarshal([]byte(body), &idx); err != nil {
		return Index{}, fmt.Errorf("skills: parse %s: %w", IndexURI, err)
	}
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
func (c *Client) ReadSkillURI(uri string) ([]byte, error) {
	result, err := c.mcp.ReadResourceFull(uri)
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", uri, err)
	}
	return extractBytes(result.Contents, uri)
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
func (c *Client) ReadSkillManifest(uri string) (*SkillManifest, error) {
	parts, err := ParseURI(uri)
	if err != nil {
		return nil, fmt.Errorf("skills: %s: %w", uri, err)
	}
	if !parts.IsManifest {
		return nil, fmt.Errorf("%w: %q", ErrNotManifestURI, uri)
	}
	raw, err := c.ReadSkillURI(uri)
	if err != nil {
		return nil, err
	}
	fm, body, err := ParseFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("skills: parse frontmatter for %s: %w", uri, err)
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
// Returns the same byte semantics as ReadSkillURI.
func (c *Client) ReadSkillFile(manifest *SkillManifest, relPath string) ([]byte, error) {
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
	return c.ReadSkillURI(resolved.String())
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
func (c *Client) ReadAndVerify(uri, expectedDigest string) (*ReadResult, error) {
	body, err := c.ReadSkillURI(uri)
	if err != nil {
		return nil, err
	}
	result := &ReadResult{URI: uri, Bytes: body}
	if expectedDigest == "" {
		return result, nil
	}
	sum := sha256.Sum256(body)
	got := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, expectedDigest) {
		return nil, fmt.Errorf("%w: want %s, got %s", ErrDigestMismatch, expectedDigest, got)
	}
	result.DigestVerified = true
	return result, nil
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
