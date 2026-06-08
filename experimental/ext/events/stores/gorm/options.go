package gormstore

// Option configures a GORM-backed store at construction.
type Option func(*config)

type config struct {
	skipAutoMigrate bool
}

func defaultConfig() *config { return &config{} }

// WithoutAutoMigrate disables the AutoMigrate call at store
// construction. Use this in production where schema changes are
// managed by an out-of-band migration tool — AutoMigrate is the
// dev / demo default but is not the long-term schema-evolution story.
func WithoutAutoMigrate() Option {
	return func(c *config) { c.skipAutoMigrate = true }
}
