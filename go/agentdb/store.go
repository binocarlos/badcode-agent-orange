package agentdb

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store is a concrete Postgres-backed store for agent data. It manages its own
// connection pool and runs migrations on startup.
type Store struct {
	gdb *gorm.DB
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
