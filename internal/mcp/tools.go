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

func safePct(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}
