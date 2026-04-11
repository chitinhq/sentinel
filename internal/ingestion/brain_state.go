package ingestion

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// BrainStateAdapter takes periodic snapshots of Redis brain state and
// converts them to synthetic ExecutionEvents for historical analysis.
type BrainStateAdapter struct {
	redis    *redis.Client
	interval time.Duration // minimum time between snapshots (default 5m)
}

// NewBrainStateAdapter constructs a BrainStateAdapter.
func NewBrainStateAdapter(redisClient *redis.Client, interval time.Duration) *BrainStateAdapter {
	if interval == 0 {
		interval = 5 * time.Minute
	}
	return &BrainStateAdapter{redis: redisClient, interval: interval}
}

// Ingest takes a snapshot of brain state if enough time has elapsed since
// the last snapshot. Returns 0 or 1 events.
func (a *BrainStateAdapter) Ingest(ctx context.Context, cp *Checkpoint) ([]ExecutionEvent, *Checkpoint, error) {
	// Check rate limit.
	if cp != nil && !cp.LastRunAt.IsZero() {
		if time.Since(cp.LastRunAt) < a.interval {
			return nil, cp, nil
		}
	}

	now := time.Now().UTC()
	tags := make(map[string]string)

	// Skip list.
	skipList, err := a.redis.HGetAll(ctx, "octi:skip-list").Result()
	if err != nil && err != redis.Nil {
		return nil, cp, fmt.Errorf("read skip list: %w", err)
	}
	tags["skip_list_size"] = strconv.Itoa(len(skipList))
	if len(skipList) > 0 {
		items := make([]string, 0, len(skipList))
		for k := range skipList {
			items = append(items, k)
		}
		// Limit to first 20 items to avoid huge tags.
		if len(items) > 20 {
			items = items[:20]
		}
		joined := ""
		for i, item := range items {
			if i > 0 {
				joined += ","
			}
			joined += item
		}
		tags["skip_list_items"] = joined
	}

	// Dispatch counts per platform.
	for _, platform := range []string{"claude", "copilot", "gemini", "codex"} {
		count, err := a.redis.Get(ctx, "octi:dispatch-count:"+platform).Result()
		if err == redis.Nil {
			count = "0"
		} else if err != nil {
			count = "0"
		}
		tags["dispatch_count_"+platform] = count
	}

	// Budget per platform.
	for _, platform := range []string{"claude", "copilot", "gemini", "codex"} {
		budget, err := a.redis.HGetAll(ctx, "octi:budget:"+platform).Result()
		if err != nil || len(budget) == 0 {
			continue
		}
		if cap, ok := budget["daily_cap"]; ok {
			tags["budget_"+platform+"_cap"] = cap
		}
		if throttled, ok := budget["throttled"]; ok {
			tags["budget_"+platform+"_throttled"] = throttled
		}
		// Compute budget percentage.
		dispatchCount, _ := strconv.Atoi(tags["dispatch_count_"+platform])
		dailyCap, _ := strconv.Atoi(budget["daily_cap"])
		if dailyCap > 0 {
			pct := (dispatchCount * 100) / dailyCap
			tags["budget_"+platform+"_pct"] = strconv.Itoa(pct)
		}
	}

	// Read brain constraint.
	constraint, err := a.redis.Get(ctx, "octi:constraint").Result()
	if err == nil && constraint != "" {
		tags["constraint"] = constraint
	}

	exitCode := 0
	ev := ExecutionEvent{
		ID:          fmt.Sprintf("brain-snap-%d", now.UnixMilli()),
		Timestamp:   now,
		Source:      SourceBrainState,
		SessionID:   fmt.Sprintf("brain-%s", now.Format("2006-01-02")),
		SequenceNum: 0,
		Actor:       ActorAgent,
		AgentID:     "brain",
		Command:     "brain:snapshot",
		ExitCode:    &exitCode,
		HasError:    false,
		Tags:        tags,
	}

	newCp := &Checkpoint{
		Adapter:   "brain_state",
		LastRunID: fmt.Sprintf("%d", now.UnixMilli()),
		LastRunAt: now,
	}
	return []ExecutionEvent{ev}, newCp, nil
}
