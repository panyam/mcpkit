// Package surfaces holds wiring shared by the agent host surfaces (the
// terminal chat in agent/surfaces/chat and the web bridge in
// agent/surfaces/web). It is the common home for surface-agnostic
// construction helpers so a surface picks a backend from a single spec
// string rather than each re-deriving the mapping.
package surfaces

import (
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/panyam/mcpkit/agent"
	gormstore "github.com/panyam/mcpkit/agent/store/gorm"
	redisstore "github.com/panyam/mcpkit/agent/store/redis"
)

// RunStoreFromSpec maps a session-store spec to an agent.RunStore backend.
// The spec vocabulary is:
//
//   - "" (empty)            persistence off; returns (nil, nil) so the caller
//     leaves the host on its non-persisting default.
//   - "memory"              in-process resume/fork that dies with the process.
//   - "sqlite://path.db"    restart-surviving sessions on a local file, no
//     server required.
//   - "redis://host:port"   restart-surviving sessions in a running Redis.
//   - "postgres://user:pass@host:port/db"
//     (or "postgresql://…")  restart-surviving sessions in a running Postgres.
//
// An unknown spec is an error. The mapping is shared by the chat and web
// surfaces so a single --session-store flag reads the same everywhere.
func RunStoreFromSpec(spec string) (agent.RunStore, error) {
	switch {
	case spec == "":
		return nil, nil
	case spec == "memory":
		return agent.NewInMemoryRunStore(), nil
	case strings.HasPrefix(spec, "redis://"):
		addr := strings.TrimPrefix(spec, "redis://")
		if addr == "" {
			return nil, fmt.Errorf("surfaces: --session-store redis:// needs host:port")
		}
		return redisstore.New(redis.NewClient(&redis.Options{Addr: addr})), nil
	case strings.HasPrefix(spec, "sqlite://"):
		path := strings.TrimPrefix(spec, "sqlite://")
		if path == "" {
			return nil, fmt.Errorf("surfaces: --session-store sqlite:// needs a file path")
		}
		return openGormStore(sqlite.Open(path + "?_busy_timeout=5000"))
	case strings.HasPrefix(spec, "postgres://") || strings.HasPrefix(spec, "postgresql://"):
		// gorm's postgres driver accepts the URL DSN verbatim.
		return openGormStore(postgres.Open(spec))
	default:
		return nil, fmt.Errorf("surfaces: unknown --session-store %q (want memory, sqlite://path.db, redis://host:port, or postgres://user:pass@host:port/db)", spec)
	}
}

// openGormStore opens the dialector with SQL logging silenced (a surface's
// transcript is its output; slow-query noise does not belong there) and
// wraps it in the RunStore.
func openGormStore(dial gorm.Dialector) (agent.RunStore, error) {
	db, err := gorm.Open(dial, &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("surfaces: opening store: %w", err)
	}
	return gormstore.New(db)
}
