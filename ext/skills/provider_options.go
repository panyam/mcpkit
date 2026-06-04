package skills

import (
	"io/fs"
	"os"
	"time"
)

// ProviderOption configures a Provider via NewProvider.
type ProviderOption func(*providerConfig)

type providerConfig struct {
	fsys           fs.FS
	root           string
	uriPrefix      []string
	metaPrefix     string
	suppressIndex  bool
	indexCacheTTL  time.Duration
}

// WithFS supplies the io/fs.FS that the Provider walks for skills. The
// FS is rooted at "." within itself. Use any fs.FS implementation:
// os.DirFS for a local directory, embed.FS for a binary-embedded tree,
// fstest.MapFS for tests, or a chained adapter that synthesizes content
// (e.g., generating SKILL.md for non-conforming directories).
func WithFS(fsys fs.FS) ProviderOption {
	return func(c *providerConfig) {
		c.fsys = fsys
		c.root = "."
	}
}

// WithDirectory is sugar for WithFS(os.DirFS(path)). It exists for the
// common local-directory case so callers do not have to spell out the
// os.DirFS wrap.
func WithDirectory(path string) ProviderOption {
	return func(c *providerConfig) {
		c.fsys = os.DirFS(path)
		c.root = "."
	}
}

// WithURIPrefix sets the organizational prefix that segments every
// skill's URI under. Per SEP-2640, servers MAY organize skills
// hierarchically. With prefix "acme/billing" a skill named "refunds"
// becomes skill://acme/billing/refunds/SKILL.md instead of
// skill://refunds/SKILL.md.
//
// The prefix is split on "/". An empty prefix means no prefix.
func WithURIPrefix(prefix string) ProviderOption {
	return func(c *providerConfig) {
		c.uriPrefix = splitNonEmpty(prefix, "/")
	}
}

// WithMetaPrefix overrides the reverse-domain prefix used to surface
// extra SKILL.md frontmatter fields through a resource's annotations.
// SEP-2640 recommends "io.modelcontextprotocol.skills/" and that is the
// default when this option is not supplied.
func WithMetaPrefix(prefix string) ProviderOption {
	return func(c *providerConfig) {
		c.metaPrefix = prefix
	}
}

// WithoutIndex suppresses the auto-registration of skill://index.json
// when the Provider's RegisterWith is called. Use this when the server
// wants to expose individual skill files but not the discovery index
// (e.g., a generated catalog the SEP says hosts MUST NOT treat absence
// as proof of "no skills"), or when the caller wants to construct and
// register an Indexer explicitly with non-default options.
func WithoutIndex() ProviderOption {
	return func(c *providerConfig) {
		c.suppressIndex = true
	}
}

// WithIndexCacheTTL forwards a cache TTL to the Indexer that
// Provider.RegisterWith builds for skill://index.json. Equivalent to
// constructing an Indexer explicitly with WithIndexerCacheTTL(d).
// Ignored when WithoutIndex is also supplied. See Indexer for the full
// cache semantics including the zero-mtime fallback.
func WithIndexCacheTTL(d time.Duration) ProviderOption {
	return func(c *providerConfig) {
		c.indexCacheTTL = d
	}
}
