package db_test

import (
	"context"
	"testing"

	"github.com/AgentGuardHQ/sentinel/internal/db"
)

func TestNeonClient_Interface(t *testing.T) {
	// Verify NeonClient implements EventStore
	var _ db.EventStore = (*db.NeonClient)(nil)
}

func TestNeonClient_ConnectFailsGracefully(t *testing.T) {
	_, err := db.NewNeonClient(context.Background(), "postgres://invalid:5432/nonexistent")
	if err == nil {
		t.Fatal("expected connection error for invalid URL")
	}
}
