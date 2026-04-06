package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ChitinEvent matches the JSONL format emitted by chitin's hook/emit.go.
type ChitinEvent struct {
	Timestamp string `json:"ts"`
	SessionID string `json:"sid"`
	Agent     string `json:"agent"`
	Tool      string `json:"tool"`
	Action    string `json:"action"`
	Path      string `json:"path,omitempty"`
	Command   string `json:"command,omitempty"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	Source    string `json:"source,omitempty"`
	LatencyUs int64  `json:"latency_us"`
}

// IngestFile reads a JSONL file and inserts events into governance_events.
// Returns the number of events ingested.
func IngestFile(pool *pgxpool.Pool, path string, tenantID string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open events file: %w", err)
	}
	defer f.Close()

	ctx := context.Background()
	count := 0
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var ev ChitinEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // skip malformed lines
		}

		metadata, _ := json.Marshal(map[string]any{
			"command":    ev.Command,
			"source":     ev.Source,
			"latency_us": ev.LatencyUs,
			"reason":     ev.Reason,
		})

		riskLevel := "low"
		if ev.Outcome == "deny" {
			riskLevel = "medium"
			if ev.Source == "invariant" {
				riskLevel = "high"
			}
		}

		_, err := pool.Exec(ctx, `
			INSERT INTO governance_events
				(tenant_id, session_id, agent_id, event_type, action, resource, outcome, risk_level, event_source, driver_type, metadata, timestamp)
			VALUES
				($1, $2, $3, 'tool_call', $4, $5, $6, $7, 'agent', $8, $9::jsonb, $10::timestamptz)
		`,
			tenantID,
			ev.SessionID,
			ev.Agent,
			ev.Tool,
			coalesce(ev.Path, ev.Command),
			ev.Outcome,
			riskLevel,
			ev.Agent,
			string(metadata),
			ev.Timestamp,
		)
		if err != nil {
			return count, fmt.Errorf("insert event: %w", err)
		}
		count++
	}

	return count, scanner.Err()
}

func coalesce(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
