package gormstore

// Option configures a RunStore at construction.
type Option func(*config)

type config struct {
	skipAutoMigrate bool
}

// WithoutAutoMigrate disables the AutoMigrate call at store
// construction. Use this in production where schema changes are managed
// by an out-of-band migration tool — AutoMigrate is the dev / demo
// default, not the long-term schema-evolution story.
func WithoutAutoMigrate() Option {
	return func(c *config) { c.skipAutoMigrate = true }
}
