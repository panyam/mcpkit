package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/panyam/mcpkit/core"
)

// maxSkillsListPages bounds cursor-following in ListSkillEntries so a server
// emitting an unbounded cursor surfaces as ErrSkillsListOverrun rather than
// spinning forever. Mirrors the client's WithMaxListPages default.
const maxSkillsListPages = 1000

var (
	// ErrSkillsListOverrun is returned when skills/list kept yielding a
	// nextCursor past maxSkillsListPages. The entries gathered before the
	// bound are discarded: a truncated listing that looks complete is worse
	// than an error, because a host would read it as the server's full
	// catalog.
	ErrSkillsListOverrun = errors.New("skills: skills/list exceeded the page bound")

	// ErrSizeMismatch is returned when served bytes differ in length from the
	// size the entry pinned.
	//
	// SEP-2640 makes this a verification failure in its own right, equal in
	// standing to a digest mismatch and independent of whether the digest was
	// computed. A host MUST NOT use the bytes.
	ErrSizeMismatch = errors.New("skills: size mismatch — content MUST NOT be used")

	// ErrSizeMissing is returned by ReadFromEntry under
	// WithRequireResourceSize when an entry omits size, which SEP-2640 makes
	// REQUIRED. Off by default: the two shipping implementations predate the
	// field, and their digests still verify.
	ErrSizeMissing = errors.New("skills: resource entry carries no size")

	// ErrURINotInResources is returned when a read targets a URI the skill's
	// resources manifest does not list.
	//
	// The manifest is complete by definition, so an unlisted URI is not an
	// unpinned file: it is a file this skill does not contain. SEP-2640 makes
	// reading one a verification failure.
	ErrURINotInResources = errors.New("skills: URI not listed in the skill's resources — content MUST NOT be used")
)

// ListSkillEntries enumerates the server's skills via skills/list, following
// nextCursor until the listing is exhausted.
//
// An empty result is a conformant answer and does NOT mean the server has no
// skills: SEP-2640 permits an empty or partial listing, and hosts MUST NOT
// read one as proof of absence. A host that knows a skill URI from elsewhere
// should still call GetSkill for it.
//
// Entries are atomic across pages, so a skill's resources are never split.
//
// ctx parents the SEP-414 `skills.list` span when a TracerProvider is
// installed; on success the span carries mcp.skill.count.
func (c *Client) ListSkillEntries(ctx context.Context) ([]SkillEntry, error) {
	ctx, span := c.cfg.tp.StartSpan(ctx, "skills.list")
	defer span.End()

	var (
		out    []SkillEntry
		cursor string
	)
	for page := 0; ; page++ {
		if page >= maxSkillsListPages {
			err := fmt.Errorf("%w: %d pages", ErrSkillsListOverrun, page)
			span.RecordError(err)
			return nil, err
		}
		res, err := c.mcp.Call(ctx, MethodSkillsList, SkillsListRequest{Cursor: cursor})
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("skills: %s: %w", MethodSkillsList, err)
		}
		var lr SkillsListResult
		if err := res.Unmarshal(&lr); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("skills: decode %s: %w", MethodSkillsList, err)
		}
		out = append(out, lr.Skills...)
		if lr.NextCursor == "" {
			break
		}
		cursor = lr.NextCursor
	}

	span.SetAttribute("mcp.skill.count", fmt.Sprintf("%d", len(out)))
	return out, nil
}

// GetSkill fetches a single skill entry by its SKILL.md URI.
//
// Servers MAY serve a skill here that their listing omits, which is how a
// server with an unenumerable or partial catalog stays usable. A host holding
// a URI from server instructions, a user, or another skill should reach for
// this rather than scanning a listing.
//
// A URI the server does not serve comes back as a -32602 error from the
// server, propagated here.
func (c *Client) GetSkill(ctx context.Context, uri string) (SkillEntry, error) {
	ctx, span := c.cfg.tp.StartSpan(ctx, "skills.get",
		core.Attribute{Key: "mcp.skill.uri", Value: uri},
	)
	defer span.End()

	res, err := c.mcp.Call(ctx, MethodSkillsGet, SkillsGetRequest{URI: uri})
	if err != nil {
		span.RecordError(err)
		return SkillEntry{}, fmt.Errorf("skills: %s %s: %w", MethodSkillsGet, uri, err)
	}
	var gr SkillsGetResult
	if err := res.Unmarshal(&gr); err != nil {
		span.RecordError(err)
		return SkillEntry{}, fmt.Errorf("skills: decode %s: %w", MethodSkillsGet, err)
	}
	return gr.Skill, nil
}

// ReadFromEntry reads uri and verifies it against entry's resource manifest,
// checking every obligation SEP-2640 places on a host read.
//
// The checks, in the order a host should care about them:
//
//   - The entry must be loadable at all. An entry whose resources is neither
//     an array nor "dynamic" is invalid and yields ErrInvalidResources.
//   - uri must appear in the manifest. The manifest is complete, so an
//     unlisted URI is a file the skill does not contain, not merely an
//     unpinned one: ErrURINotInResources.
//   - The served length must equal the pinned size: ErrSizeMismatch. This is
//     checked before hashing, since it is the cheaper rejection and the SEP
//     makes it a failure whether or not the digest is computed.
//   - The served bytes must hash to the pinned digest: ErrDigestMismatch.
//
// For a "dynamic" manifest there is nothing to verify against, so the read
// proceeds unverified and the result reports DigestVerified false. That is
// the sentinel's whole purpose: a server that cannot pin its content says so
// explicitly rather than omitting the field.
func (c *Client) ReadFromEntry(ctx context.Context, entry SkillEntry, uri string) (*ReadResult, error) {
	if entry.Resources.Dynamic {
		return c.ReadAndVerify(ctx, uri, "")
	}
	if entry.Resources.Files == nil {
		return nil, fmt.Errorf("%w: entry %s", ErrInvalidResources, entry.URI)
	}

	var want *SkillResource
	for i := range entry.Resources.Files {
		if entry.Resources.Files[i].URI == uri {
			want = &entry.Resources.Files[i]
			break
		}
	}
	if want == nil {
		return nil, fmt.Errorf("%w: %s not in %s", ErrURINotInResources, uri, entry.URI)
	}

	// One read, then both checks over the same bytes. Verifying length and
	// digest against two separate fetches would let a server pass by serving
	// different content to each.
	body, err := c.ReadSkillURI(ctx, uri)
	if err != nil {
		return nil, err
	}
	if want.Size == nil {
		// The entry pins no length. Refusing here would make mcpkit unable to
		// read from any server pinned to a SEP revision before 2026-08-20,
		// and would push hosts toward disabling verification wholesale. The
		// digest below still guarantees content integrity; size is a cheaper
		// early rejection, not the integrity mechanism. Opt into strictness
		// with WithRequireResourceSize.
		if c.cfg.requireResourceSize {
			return nil, fmt.Errorf("%w: %s carries no size", ErrSizeMissing, uri)
		}
	} else if got := int64(len(body)); got != *want.Size {
		return nil, fmt.Errorf("%w: %s: want %d bytes, got %d", ErrSizeMismatch, uri, *want.Size, got)
	}
	sum := sha256.Sum256(body)
	if got := "sha256:" + hex.EncodeToString(sum[:]); !strings.EqualFold(got, want.Digest) {
		return nil, fmt.Errorf("%w: %s: want %s, got %s", ErrDigestMismatch, uri, want.Digest, got)
	}
	return &ReadResult{URI: uri, Bytes: body, DigestVerified: true}, nil
}
