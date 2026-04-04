package ingestion

import (
	"context"
	"testing"
	"time"
)

type mockDB struct {
	inserted   []ExecutionEvent
	checkpoint *Checkpoint
}

func (m *mockDB) InsertExecutionEvents(ctx context.Context, events []ExecutionEvent) (int, error) {
	m.inserted = append(m.inserted, events...)
	return len(events), nil
}

func (m *mockDB) GetCheckpoint(ctx context.Context, adapter string) (*Checkpoint, error) {
	return m.checkpoint, nil
}

func (m *mockDB) UpsertCheckpoint(ctx context.Context, cp Checkpoint) error {
	m.checkpoint = &cp
	return nil
}

func TestStoreWriteEvents(t *testing.T) {
	db := &mockDB{}
	store := NewStore(db)

	exitCode := 1
	events := []ExecutionEvent{
		{
			ID:        "ev-1",
			Timestamp: time.Now(),
			Source:    SourceGitHubActions,
			SessionID: "run-1",
			Command:   "npm test",
			ExitCode:  &exitCode,
			HasError:  true,
		},
	}

	n, err := store.Write(context.Background(), events)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 1 {
		t.Errorf("wrote %d, want 1", n)
	}
	if len(db.inserted) != 1 {
		t.Errorf("inserted %d, want 1", len(db.inserted))
	}
}

func TestStoreWriteEmpty(t *testing.T) {
	db := &mockDB{}
	store := NewStore(db)
	n, err := store.Write(context.Background(), nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 0 {
		t.Errorf("wrote %d, want 0", n)
	}
}

func TestStoreCheckpoint(t *testing.T) {
	db := &mockDB{}
	store := NewStore(db)
	ctx := context.Background()

	cp, err := store.GetCheckpoint(ctx, "github_actions")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp != nil {
		t.Errorf("expected nil checkpoint, got %+v", cp)
	}

	now := time.Now()
	err = store.SaveCheckpoint(ctx, Checkpoint{
		Adapter:   "github_actions",
		LastRunID: "run-42",
		LastRunAt: now,
	})
	if err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	cp, err = store.GetCheckpoint(ctx, "github_actions")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if cp == nil {
		t.Fatal("expected checkpoint, got nil")
	}
	if cp.LastRunID != "run-42" {
		t.Errorf("LastRunID = %q, want %q", cp.LastRunID, "run-42")
	}
}
