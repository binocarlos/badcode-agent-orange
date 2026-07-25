package agentdb

import (
	"fmt"
	"sync"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is a concrete Postgres-backed store for agent data. It manages its own
// connection pool and runs migrations on startup.
//
// Config-log invariant (§15.4): every method that mutates *project
// configuration* — workers, project settings and prompts, subscriptions,
// schedules, images, skills — writes its projection row and its `config_events`
// record in one transaction, via WithConfigEvent. See the adoption recipe at
// the top of config_events.go; TestMutationsAreLogged enforces it.
type Store struct {
	gdb *gorm.DB

	// memVecOnce/memVecOK cache whether the pgvector column on `memories`
	// exists (migration 022 adds it only where the extension is available).
	memVecOnce sync.Once
	memVecOK   bool

	// configHook is J3's post-commit seam: WithConfigEvent calls it with the
	// committed record once the transaction has landed, and the host turns that
	// into the routable `config.changed` event (§15.4). Guarded because it is
	// installed at boot while other goroutines may already be mutating.
	// See SetConfigEventHook in config_events.go.
	hookMu     sync.RWMutex
	configHook ConfigEventHook
}

// Open connects to Postgres, runs migrations, and returns a ready Store.
func Open(postgresURL string) (*Store, error) {
	gdb, err := gorm.Open(postgres.Open(postgresURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("agentdb: connect: %w", err)
	}
	if err := runMigrations(gdb); err != nil {
		return nil, fmt.Errorf("agentdb: migrations: %w", err)
	}
	return &Store{gdb: gdb}, nil
}

// MustOpen is like Open but panics on error.
func MustOpen(postgresURL string) *Store {
	s, err := Open(postgresURL)
	if err != nil {
		panic(fmt.Sprintf("agentdb.MustOpen: %v", err))
	}
	return s
}

// DB returns the underlying *gorm.DB for advanced queries.
func (s *Store) DB() *gorm.DB { return s.gdb }
