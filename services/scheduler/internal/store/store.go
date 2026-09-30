package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidRequest marks a command that is permanently invalid and should be dropped.
var ErrInvalidRequest = errors.New("invalid scheduler request")

type Store struct{ db *pgxpool.Pool }

// New creates a Scheduler store backed by the PostgreSQL connection pool.
func New(db *pgxpool.Pool) *Store { return &Store{db: db} }
