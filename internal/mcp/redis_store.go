package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// RedisStore provides Redis reads for the MCP tools.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore constructs a RedisStore.
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

// --- sentinel_skip_list ---

// SkipListItem is a single item in the skip list.
type SkipListItem struct {
	Issue          string `json:"issue"`
	Reason         string `json:"reason"`
	AddedAt        string `json:"added_at,omitempty"`
	RejectionCount int    `json:"rejection_count"`
}

// SkipListResult is the response for sentinel_skip_list.
type SkipListResult struct {
	Count int            `json:"count"`
	Items []SkipListItem `json:"items"`
}

// GetSkipList returns the contents of the brain's skip list.
func (r *RedisStore) GetSkipList(ctx context.Context) (*SkipListResult, error) {
	all, err := r.client.HGetAll(ctx, "octi:skip-list").Result()
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("read skip list: %w", err)
	}

	result := &SkipListResult{
		Count: len(all),
		Items: make([]SkipListItem, 0, len(all)),
	}

	for issue, val := range all {
		item := SkipListItem{Issue: issue}
		// Try to parse value as JSON.
		var parsed struct {
			Reason         string `json:"reason"`
			AddedAt        string `json:"added_at"`
			RejectionCount int    `json:"rejection_count"`
		}
		if err := json.Unmarshal([]byte(val), &parsed); err == nil {
			item.Reason = parsed.Reason
			item.AddedAt = parsed.AddedAt
			item.RejectionCount = parsed.RejectionCount
		} else {
			// Value is plain string reason.
			item.Reason = val
		}
		result.Items = append(result.Items, item)
	}

	return result, nil
}

// --- sentinel_insights (Redis cache) ---

// CachedInsight is the shape stored in octi:insights:latest.
type CachedInsight struct {
	ID              string         `json:"id"`
	Timestamp       string         `json:"timestamp"`
	Category        string         `json:"category"`
	Severity        string         `json:"severity"`
	Narrative       string         `json:"narrative"`
	Evidence        map[string]any `json:"evidence,omitempty"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	ScopeType       string         `json:"scope_type,omitempty"`
	ScopeValue      string         `json:"scope_value,omitempty"`
}

// GetInsights reads cached insights from Redis.
func (r *RedisStore) GetInsights(ctx context.Context) ([]CachedInsight, error) {
	data, err := r.client.Get(ctx, "octi:insights:latest").Result()
	if err != nil {
		return nil, err
	}
	var insights []CachedInsight
	if err := json.Unmarshal([]byte(data), &insights); err != nil {
		return nil, fmt.Errorf("unmarshal cached insights: %w", err)
	}
	return insights, nil
}

// --- sentinel_budget ---

// BudgetRow is per-platform budget info.
type BudgetRow struct {
	Platform       string `json:"platform"`
	DispatchesToday int   `json:"dispatches_today"`
	DailyCap       int    `json:"daily_cap"`
	UsagePct       int    `json:"usage_pct"`
	Throttled      bool   `json:"throttled"`
	CycleResets    string `json:"cycle_resets,omitempty"`
}

// BudgetResult is the response for sentinel_budget.
type BudgetResult struct {
	Platforms []BudgetRow `json:"platforms"`
}

// GetBudget returns budget and dispatch counters per platform.
func (r *RedisStore) GetBudget(ctx context.Context, platform string) (*BudgetResult, error) {
	platforms := []string{"claude", "copilot", "gemini", "codex"}
	if platform != "" {
		platforms = []string{platform}
	}

	result := &BudgetResult{
		Platforms: make([]BudgetRow, 0, len(platforms)),
	}

	for _, p := range platforms {
		row := BudgetRow{Platform: p}

		// Dispatch count.
		countStr, err := r.client.Get(ctx, "octi:dispatch-count:"+p).Result()
		if err == nil {
			row.DispatchesToday, _ = strconv.Atoi(countStr)
		}

		// Budget hash.
		budget, err := r.client.HGetAll(ctx, "octi:budget:"+p).Result()
		if err == nil && len(budget) > 0 {
			if cap, ok := budget["daily_cap"]; ok {
				row.DailyCap, _ = strconv.Atoi(cap)
			}
			if t, ok := budget["throttled"]; ok {
				row.Throttled = (t == "true" || t == "1")
			}
			if cr, ok := budget["cycle_resets"]; ok {
				row.CycleResets = cr
			}
		}

		if row.DailyCap > 0 {
			row.UsagePct = (row.DispatchesToday * 100) / row.DailyCap
		}

		result.Platforms = append(result.Platforms, row)
	}

	return result, nil
}
