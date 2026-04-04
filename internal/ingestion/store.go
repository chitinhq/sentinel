package ingestion

import (
	"context"
	"time"
)

type Checkpoint struct {
	Adapter   string
	LastRunID string
	LastRunAt time.Time
}

type DB interface {
	InsertExecutionEvents(ctx context.Context, events []ExecutionEvent) (int, error)
	GetCheckpoint(ctx context.Context, adapter string) (*Checkpoint, error)
	UpsertCheckpoint(ctx context.Context, cp Checkpoint) error
}

type Store struct {
	db DB
}

func NewStore(db DB) *Store {
	return &Store{db: db}
}

func (s *Store) Write(ctx context.Context, events []ExecutionEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	return s.db.InsertExecutionEvents(ctx, events)
}

func (s *Store) GetCheckpoint(ctx context.Context, adapter string) (*Checkpoint, error) {
	return s.db.GetCheckpoint(ctx, adapter)
}

func (s *Store) SaveCheckpoint(ctx context.Context, cp Checkpoint) error {
	return s.db.UpsertCheckpoint(ctx, cp)
}
