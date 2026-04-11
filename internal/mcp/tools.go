package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RegisterTools adds all sentinel MCP tools to the server.
func RegisterTools(s *Server, pool *pgxpool.Pool, tenantID string) {
	s.Register(&Tool{
		Name:        "sentinel_ingest",
		Description: "Flush chitin governance events from local JSONL file to Neon database",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"workspace": map[string]any{
					"type":        "string",
					"description": "Workspace directory containing .chitin/events.jsonl",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		workspace, _ := args["workspace"].(string)
		if workspace == "" {
			workspace = os.Getenv("CHITIN_WORKSPACE")
		}
		if workspace == "" {
			return "", fmt.Errorf("workspace not specified and CHITIN_WORKSPACE not set")
		}

		path := filepath.Join(workspace, ".chitin", "events.jsonl")
		if _, err := os.Stat(path); err != nil {
			return "No events file found — nothing to ingest.", nil
		}

		count, err := IngestFile(pool, path, tenantID)
		if err != nil {
			return "", fmt.Errorf("ingest failed: %w", err)
		}

		// Truncate the file after successful ingestion
		os.Truncate(path, 0)

		return fmt.Sprintf("Ingested %d events from %s", count, path), nil
	})

	s.Register(&Tool{
		Name:        "sentinel_recent",
		Description: "Query recent governance events from the database",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type":        "number",
					"description": "Max events to return (default 20)",
				},
				"hours": map[string]any{
					"type":        "number",
					"description": "Look back N hours (default 24)",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		limit := intArg(args, "limit", 20)
		hours := intArg(args, "hours", 24)
		since := time.Now().Add(-time.Duration(hours) * time.Hour)

		rows, err := pool.Query(context.Background(), `
			SELECT timestamp, agent_id, action, resource, outcome, risk_level
			FROM governance_events
			WHERE tenant_id = $1 AND timestamp >= $2
			ORDER BY timestamp DESC
			LIMIT $3
		`, tenantID, since, limit)
		if err != nil {
			return "", err
		}
		defer rows.Close()

		var lines []string
		lines = append(lines, fmt.Sprintf("Recent events (last %dh, limit %d):", hours, limit))
		for rows.Next() {
			var ts time.Time
			var agent, action, resource, outcome, risk string
			rows.Scan(&ts, &agent, &action, &resource, &outcome, &risk)
			lines = append(lines, fmt.Sprintf("  %s | %-12s | %-8s | %-6s | %s | %s",
				ts.Format("15:04:05"), agent, action, outcome, risk, truncStr(resource, 60)))
		}

		if len(lines) == 1 {
			lines = append(lines, "  (no events found)")
		}

		return strings.Join(lines, "\n"), nil
	})

	s.Register(&Tool{
		Name:        "sentinel_denials",
		Description: "Show denied actions with reasons from recent governance events",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hours": map[string]any{
					"type":        "number",
					"description": "Look back N hours (default 24)",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		hours := intArg(args, "hours", 24)
		since := time.Now().Add(-time.Duration(hours) * time.Hour)

		rows, err := pool.Query(context.Background(), `
			SELECT timestamp, agent_id, action, resource, metadata->>'reason' as reason
			FROM governance_events
			WHERE tenant_id = $1 AND outcome = 'deny' AND timestamp >= $2
			ORDER BY timestamp DESC
			LIMIT 50
		`, tenantID, since)
		if err != nil {
			return "", err
		}
		defer rows.Close()

		var lines []string
		lines = append(lines, fmt.Sprintf("Denied actions (last %dh):", hours))
		count := 0
		for rows.Next() {
			var ts time.Time
			var agent, action, resource string
			var reason *string
			rows.Scan(&ts, &agent, &action, &resource, &reason)
			r := ""
			if reason != nil {
				r = *reason
			}
			lines = append(lines, fmt.Sprintf("  %s | %s | %s | %s | %s",
				ts.Format("15:04:05"), agent, action, truncStr(resource, 40), truncStr(r, 80)))
			count++
		}

		if count == 0 {
			lines = append(lines, "  (no denials found)")
		}

		return strings.Join(lines, "\n"), nil
	})

	s.Register(&Tool{
		Name:        "sentinel_hotspots",
		Description: "Show top denied actions ranked by frequency",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hours": map[string]any{
					"type":        "number",
					"description": "Look back N hours (default 24)",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		hours := intArg(args, "hours", 24)
		since := time.Now().Add(-time.Duration(hours) * time.Hour)

		rows, err := pool.Query(context.Background(), `
			SELECT action, COUNT(*) as total,
			       COUNT(*) FILTER (WHERE outcome = 'deny') as denials,
			       COUNT(*) FILTER (WHERE outcome = 'deny')::float / GREATEST(COUNT(*), 1) as rate
			FROM governance_events
			WHERE tenant_id = $1 AND timestamp >= $2
			GROUP BY action
			HAVING COUNT(*) FILTER (WHERE outcome = 'deny') > 0
			ORDER BY denials DESC
			LIMIT 20
		`, tenantID, since)
		if err != nil {
			return "", err
		}
		defer rows.Close()

		var lines []string
		lines = append(lines, fmt.Sprintf("Hotspots (last %dh):", hours))
		lines = append(lines, fmt.Sprintf("  %-15s %8s %8s %8s", "Action", "Total", "Denied", "Rate"))
		for rows.Next() {
			var action string
			var total, denials int
			var rate float64
			rows.Scan(&action, &total, &denials, &rate)
			lines = append(lines, fmt.Sprintf("  %-15s %8d %8d %7.1f%%", action, total, denials, rate*100))
		}

		return strings.Join(lines, "\n"), nil
	})

	s.Register(&Tool{
		Name:        "sentinel_analyze",
		Description: "Trigger a full 5-pass Sentinel analysis on recent governance data",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"hours": map[string]any{
					"type":        "number",
					"description": "Look back N hours (default 24)",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		// This calls the existing analyzer pipeline
		// For now, return summary stats as a lightweight version
		hours := intArg(args, "hours", 24)
		since := time.Now().Add(-time.Duration(hours) * time.Hour)

		var total, denials, allows int
		err := pool.QueryRow(context.Background(), `
			SELECT
				COUNT(*),
				COUNT(*) FILTER (WHERE outcome = 'deny'),
				COUNT(*) FILTER (WHERE outcome = 'allow')
			FROM governance_events
			WHERE tenant_id = $1 AND timestamp >= $2
		`, tenantID, since).Scan(&total, &denials, &allows)
		if err != nil {
			return "", err
		}

		var agents int
		pool.QueryRow(context.Background(), `
			SELECT COUNT(DISTINCT agent_id) FROM governance_events
			WHERE tenant_id = $1 AND timestamp >= $2
		`, tenantID, since).Scan(&agents)

		var sessions int
		pool.QueryRow(context.Background(), `
			SELECT COUNT(DISTINCT session_id) FROM governance_events
			WHERE tenant_id = $1 AND timestamp >= $2
		`, tenantID, since).Scan(&sessions)

		result := fmt.Sprintf(`Sentinel Analysis (last %dh):
  Events:   %d total (%d allow, %d deny)
  Agents:   %d unique
  Sessions: %d unique
  Deny rate: %.1f%%`, hours, total, allows, denials, agents, sessions,
			safePct(denials, total))

		if denials > 0 {
			result += "\n\n  Use sentinel_hotspots for breakdown by action."
			result += "\n  Use sentinel_denials for individual denial details."
		}

		return result, nil
	})

	s.Register(&Tool{
		Name:        "sentinel_status",
		Description: "Check Sentinel health — database connectivity, event counts, last ingestion",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(args map[string]any) (string, error) {
		// Check DB connectivity
		err := pool.Ping(context.Background())
		if err != nil {
			return fmt.Sprintf("Sentinel status: DB UNREACHABLE (%v)", err), nil
		}

		var total int
		pool.QueryRow(context.Background(),
			"SELECT COUNT(*) FROM governance_events WHERE tenant_id = $1", tenantID).Scan(&total)

		var latest *time.Time
		pool.QueryRow(context.Background(),
			"SELECT MAX(timestamp) FROM governance_events WHERE tenant_id = $1", tenantID).Scan(&latest)

		latestStr := "never"
		if latest != nil {
			latestStr = latest.Format(time.RFC3339)
		}

		// Check for local events file
		workspace := os.Getenv("CHITIN_WORKSPACE")
		pending := 0
		if workspace != "" {
			if data, err := os.ReadFile(filepath.Join(workspace, ".chitin", "events.jsonl")); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					if strings.TrimSpace(line) != "" {
						pending++
					}
				}
			}
		}

		return fmt.Sprintf(`Sentinel status:
  Database:      connected
  Total events:  %d
  Latest event:  %s
  Pending local: %d events (not yet ingested)`, total, latestStr, pending), nil
	})
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case json.Number:
			i, _ := n.Int64()
			return int(i)
		}
	}
	return def
}

func truncStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func filterInsights(insights []CachedInsight, category, minSeverity string, limit int) []CachedInsight {
	sevOrder := map[string]int{"info": 0, "warning": 1, "high": 2, "critical": 3}
	minOrd := 0
	if o, ok := sevOrder[minSeverity]; ok {
		minOrd = o
	}

	var filtered []CachedInsight
	for _, ins := range insights {
		if category != "" && ins.Category != category {
			continue
		}
		if minSeverity != "" {
			if o, ok := sevOrder[ins.Severity]; !ok || o < minOrd {
				continue
			}
		}
		filtered = append(filtered, ins)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered
}

func safePct(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}

// RegisterObservabilityTools adds the 6 new observability MCP tools.
// Requires: QueryStore (Neon) and optionally RedisStore.
func RegisterObservabilityTools(s *Server, qs *QueryStore, rs *RedisStore) {
	ctx := context.Background()

	// --- sentinel_health ---
	s.Register(&Tool{
		Name:        "sentinel_health",
		Description: "Get health scores for platforms, repos, or queues",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope_type": map[string]any{
					"type": "string",
					"enum": []any{"platform", "repo", "queue"},
				},
				"scope_value": map[string]any{
					"type":        "string",
					"description": "Optional; omit for all in scope_type",
				},
			},
			"required": []any{"scope_type"},
		},
	}, func(args map[string]any) (string, error) {
		scopeType, _ := args["scope_type"].(string)
		if scopeType == "" {
			return "", fmt.Errorf("scope_type is required")
		}
		scopeValue, _ := args["scope_value"].(string)

		result, err := qs.LatestHealthScores(ctx, scopeType, scopeValue)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})

	// --- sentinel_query ---
	s.Register(&Tool{
		Name:        "sentinel_query",
		Description: "Aggregated stats over execution events — what happened in the last N hours?",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source": map[string]any{
					"type": "string",
					"enum": []any{"chitin_governance", "swarm_dispatch", "github_actions", "all"},
				},
				"since": map[string]any{
					"type":        "string",
					"description": "Duration string: 24h, 7d, etc.",
				},
				"platform": map[string]any{
					"type":        "string",
					"description": "Optional filter on agent_id",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional filter on repository",
				},
				"has_error": map[string]any{
					"type":        "boolean",
					"description": "Optional: filter to errors only",
				},
			},
			"required": []any{"source", "since"},
		},
	}, func(args map[string]any) (string, error) {
		source, _ := args["source"].(string)
		sinceStr, _ := args["since"].(string)
		if sinceStr == "" {
			sinceStr = "24h"
		}

		params := QueryEventsParams{
			Source: source,
			Since:  parseDuration(sinceStr),
		}
		if p, ok := args["platform"].(string); ok {
			params.Platform = p
		}
		if r, ok := args["repo"].(string); ok {
			params.Repo = r
		}
		if he, ok := args["has_error"].(bool); ok {
			params.HasError = &he
		}

		result, err := qs.QueryEvents(ctx, params)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})

	// --- sentinel_failures ---
	s.Register(&Tool{
		Name:        "sentinel_failures",
		Description: "Recent individual failures with extracted reasons — why are things failing?",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"since": map[string]any{
					"type":        "string",
					"description": "Duration string: 24h, 7d (default 24h)",
				},
				"platform": map[string]any{
					"type":        "string",
					"description": "Optional filter on agent_id",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Max results (default 10, max 50)",
				},
			},
			"required": []any{"since"},
		},
	}, func(args map[string]any) (string, error) {
		sinceStr, _ := args["since"].(string)
		if sinceStr == "" {
			sinceStr = "24h"
		}
		since := time.Now().Add(-parseDuration(sinceStr))
		platform, _ := args["platform"].(string)
		limit := intArg(args, "limit", 10)
		if limit > 50 {
			limit = 50
		}

		result, err := qs.RecentFailures(ctx, since, platform, limit)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})

	// --- sentinel_trends ---
	s.Register(&Tool{
		Name:        "sentinel_trends",
		Description: "Compare a metric in current window vs 7-day baseline — is this getting better or worse?",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"metric": map[string]any{
					"type": "string",
					"enum": []any{"success_rate", "latency", "volume", "denial_rate"},
				},
				"scope_type": map[string]any{
					"type": "string",
					"enum": []any{"platform", "repo", "queue"},
				},
				"scope_value": map[string]any{
					"type":        "string",
					"description": "The platform, repo, or queue to analyze",
				},
				"window": map[string]any{
					"type":        "string",
					"description": "Time window (default 24h)",
				},
			},
			"required": []any{"metric", "scope_type", "scope_value"},
		},
	}, func(args map[string]any) (string, error) {
		metric, _ := args["metric"].(string)
		scopeType, _ := args["scope_type"].(string)
		scopeValue, _ := args["scope_value"].(string)
		window, _ := args["window"].(string)
		if window == "" {
			window = "24h"
		}

		result, err := qs.ComputeTrend(ctx, metric, scopeType, scopeValue, window)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})

	// --- sentinel_skip_list ---
	s.Register(&Tool{
		Name:        "sentinel_skip_list",
		Description: "Contents of the brain's unroutable issue list",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, func(args map[string]any) (string, error) {
		if rs == nil {
			return "", fmt.Errorf("Redis not configured")
		}
		result, err := rs.GetSkipList(ctx)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})

	// --- sentinel_insights ---
	s.Register(&Tool{
		Name:        "sentinel_insights",
		Description: "Get recent LLM-generated insights — health narratives, patterns, recommendations, anomaly alerts",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type":        "string",
					"enum":        []any{"health", "pattern", "recommendation", "anomaly"},
					"description": "Optional: filter by insight category",
				},
				"severity": map[string]any{
					"type":        "string",
					"enum":        []any{"info", "warning", "high", "critical"},
					"description": "Optional: minimum severity filter",
				},
				"limit": map[string]any{
					"type":        "number",
					"description": "Max results (default 10, max 50)",
				},
				"source": map[string]any{
					"type":        "string",
					"enum":        []any{"redis", "db"},
					"description": "Read from Redis cache (fast, last 2h) or DB (full history). Default: tries Redis first, falls back to DB.",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		category, _ := args["category"].(string)
		severity, _ := args["severity"].(string)
		limit := intArg(args, "limit", 10)
		if limit > 50 {
			limit = 50
		}
		source, _ := args["source"].(string)

		// Try Redis first (fast path).
		if source != "db" && rs != nil {
			cached, err := rs.GetInsights(ctx)
			if err == nil && len(cached) > 0 {
				filtered := filterInsights(cached, category, severity, limit)
				data, _ := json.Marshal(map[string]any{
					"source":   "redis",
					"count":    len(filtered),
					"insights": filtered,
				})
				return string(data), nil
			}
		}

		// Fall back to DB.
		if qs != nil {
			insights, err := qs.QueryInsights(ctx, category, severity, limit)
			if err != nil {
				return "", err
			}
			data, _ := json.Marshal(map[string]any{
				"source":   "db",
				"count":    len(insights),
				"insights": insights,
			})
			return string(data), nil
		}

		return "", fmt.Errorf("no data source available")
	})

	// --- sentinel_write_insight ---
	s.Register(&Tool{
		Name:        "sentinel_write_insight",
		Description: "Write an LLM-generated insight to Neon + Redis. Use after analyzing data from sentinel_health/query/failures.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"category": map[string]any{
					"type": "string",
					"enum": []any{"health", "pattern", "recommendation", "anomaly"},
				},
				"severity": map[string]any{
					"type": "string",
					"enum": []any{"info", "warning", "high", "critical"},
				},
				"narrative": map[string]any{
					"type":        "string",
					"description": "2-3 sentence natural language insight explaining the why, not just the what",
				},
				"suggested_action": map[string]any{
					"type":        "string",
					"description": "Concrete next step, or empty string if no action needed",
				},
				"scope_type": map[string]any{
					"type": "string",
					"enum": []any{"platform", "repo", "queue", "system"},
				},
				"scope_value": map[string]any{
					"type":        "string",
					"description": "e.g. 'claude', 'chitinhq/octi', 'intake', 'system'",
				},
				"evidence": map[string]any{
					"type":        "object",
					"description": "Supporting data points (scores, counts, rates)",
				},
			},
			"required": []any{"category", "severity", "narrative", "scope_type", "scope_value"},
		},
	}, func(args map[string]any) (string, error) {
		category, _ := args["category"].(string)
		severity, _ := args["severity"].(string)
		narrative, _ := args["narrative"].(string)
		suggestedAction, _ := args["suggested_action"].(string)
		scopeType, _ := args["scope_type"].(string)
		scopeValue, _ := args["scope_value"].(string)
		evidence, _ := args["evidence"].(map[string]any)

		if narrative == "" {
			return "", fmt.Errorf("narrative is required")
		}

		now := time.Now().UTC()
		id := fmt.Sprintf("ins-%s-%d", category, now.UnixNano())

		evidenceJSON, _ := json.Marshal(evidence)

		// Write to Neon.
		_, err := qs.pool.Exec(ctx, `
			INSERT INTO insights (id, timestamp, category, severity, narrative, evidence, suggested_action, scope_type, scope_value, acknowledged)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, FALSE)
		`, id, now, category, severity, narrative, evidenceJSON, suggestedAction, scopeType, scopeValue)
		if err != nil {
			return "", fmt.Errorf("write insight: %w", err)
		}

		// Update Redis cache — append to existing or create new.
		if rs != nil {
			newInsight := map[string]any{
				"id": id, "timestamp": now.Format(time.RFC3339),
				"category": category, "severity": severity,
				"narrative": narrative, "suggested_action": suggestedAction,
				"scope_type": scopeType, "scope_value": scopeValue,
				"evidence": evidence,
			}

			// Read existing, append, rewrite.
			existing, _ := rs.GetInsights(ctx)
			merged := []any{newInsight}
			for _, e := range existing {
				merged = append(merged, e)
			}
			if len(merged) > 20 {
				merged = merged[:20]
			}
			data, _ := json.Marshal(merged)
			rs.client.Set(ctx, "octi:insights:latest", string(data), 2*time.Hour)

			if severity == "high" || severity == "critical" {
				rs.client.Incr(ctx, "octi:insights:unacked")
				rs.client.Expire(ctx, "octi:insights:unacked", 24*time.Hour)
			}
		}

		result := map[string]any{
			"id":       id,
			"written":  true,
			"category": category,
			"severity": severity,
		}
		if severity == "high" || severity == "critical" {
			result["ntfy"] = "would notify ganglia (not sent via MCP — use sentinel analyze for auto-notify)"
		}
		out, _ := json.Marshal(result)
		return string(out), nil
	})

	// --- sentinel_budget ---
	s.Register(&Tool{
		Name:        "sentinel_budget",
		Description: "Platform budget and dispatch counters — how much runway is left today?",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"platform": map[string]any{
					"type":        "string",
					"description": "Optional; omit for all platforms",
				},
			},
		},
	}, func(args map[string]any) (string, error) {
		if rs == nil {
			return "", fmt.Errorf("Redis not configured")
		}
		platform, _ := args["platform"].(string)
		result, err := rs.GetBudget(ctx, platform)
		if err != nil {
			return "", err
		}
		data, _ := json.Marshal(result)
		return string(data), nil
	})
}
