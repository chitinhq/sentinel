package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// These tests require a running Redis instance.
// Skip if Redis is not available.

func getTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379", DB: 15}) // use DB 15 for tests
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	t.Cleanup(func() {
		client.FlushDB(context.Background())
		client.Close()
	})
	return client
}

func TestBrainStateAdapter_Ingest(t *testing.T) {
	client := getTestRedis(t)
	ctx := context.Background()

	// Seed Redis state.
	client.HSet(ctx, "octi:skip-list", "octi#119", `{"reason":"No matching agent"}`, "chitin#5", `{"reason":"Stale branch"}`)
	client.Set(ctx, "octi:dispatch-count:claude", "4", 0)
	client.Set(ctx, "octi:dispatch-count:gemini", "1", 0)
	client.HSet(ctx, "octi:budget:claude", "daily_cap", "10", "throttled", "false")

	adapter := NewBrainStateAdapter(client, 0) // no interval for test
	events, cp, err := adapter.Ingest(ctx, nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 snapshot event, got %d", len(events))
	}

	ev := events[0]
	if ev.Source != SourceBrainState {
		t.Errorf("expected source brain_state, got %s", ev.Source)
	}
	if ev.Command != "brain:snapshot" {
		t.Errorf("expected command brain:snapshot, got %q", ev.Command)
	}
	if ev.Tags["skip_list_size"] != "2" {
		t.Errorf("expected skip_list_size=2, got %q", ev.Tags["skip_list_size"])
	}
	if ev.Tags["dispatch_count_claude"] != "4" {
		t.Errorf("expected dispatch_count_claude=4, got %q", ev.Tags["dispatch_count_claude"])
	}
	if ev.Tags["dispatch_count_gemini"] != "1" {
		t.Errorf("expected dispatch_count_gemini=1, got %q", ev.Tags["dispatch_count_gemini"])
	}
	if ev.Tags["budget_claude_pct"] != "40" {
		t.Errorf("expected budget_claude_pct=40, got %q", ev.Tags["budget_claude_pct"])
	}

	if cp == nil {
		t.Fatal("expected non-nil checkpoint")
	}
}

func TestBrainStateAdapter_RateLimit(t *testing.T) {
	client := getTestRedis(t)
	ctx := context.Background()

	adapter := NewBrainStateAdapter(client, 5*time.Minute)

	// First ingest succeeds.
	events1, cp1, err := adapter.Ingest(ctx, nil)
	if err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if len(events1) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events1))
	}

	// Second ingest within interval returns 0 events.
	events2, _, err := adapter.Ingest(ctx, cp1)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if len(events2) != 0 {
		t.Errorf("expected 0 events within rate limit, got %d", len(events2))
	}
}
