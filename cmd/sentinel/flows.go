package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// runFlows reports per-flow health by rolling up governance_events rows
// whose action starts with "flow_" (emitted by the internal/flow package).
//
// Output is a plain text table: flow name, last success, last failure, and a
// verdict (OK/STALE/FAILING) derived from the two timestamps. No new schema,
// no new dashboard — the stream is the dashboard.
func runFlows() error {
	ctx := context.Background()
	url := os.Getenv("NEON_DATABASE_URL")
	if url == "" {
		return fmt.Errorf("NEON_DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	// Flow events are identified by the 'flow.' action prefix. Using action
	// rather than event_source is durable across the ingester's current
	// hardcoding of event_source='agent' (tracked as a separate issue).
	rows, err := pool.Query(ctx, `
		SELECT
		  action AS flow,
		  MAX(CASE WHEN outcome = 'allow' THEN timestamp END) AS last_ok,
		  MAX(CASE WHEN outcome = 'deny'  THEN timestamp END) AS last_fail,
		  COUNT(*) FILTER (WHERE outcome = 'allow' AND timestamp > NOW() - INTERVAL '24 hours') AS ok_24h,
		  COUNT(*) FILTER (WHERE outcome = 'deny'  AND timestamp > NOW() - INTERVAL '24 hours') AS fail_24h
		FROM governance_events
		WHERE action LIKE 'flow.%'
		GROUP BY action
		ORDER BY action
	`)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "FLOW\tLAST_OK\tLAST_FAIL\tOK_24H\tFAIL_24H\tSTATUS")

	flowCount := 0
	for rows.Next() {
		var flow string
		var lastOK, lastFail *time.Time
		var ok24, fail24 int
		if err := rows.Scan(&flow, &lastOK, &lastFail, &ok24, &fail24); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n",
			flow,
			formatTs(lastOK),
			formatTs(lastFail),
			ok24, fail24,
			verdict(lastOK, lastFail, ok24, fail24),
		)
		flowCount++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iter: %w", err)
	}

	if flowCount == 0 {
		fmt.Fprintln(w, "(no flow events found — call flow.Emit from your components to populate)\t\t\t\t\t")
	}
	return w.Flush()
}

func formatTs(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

// verdict collapses a flow's state into a single label a human can scan.
//   OK       — at least one success in the last 24h, no more recent failure
//   FAILING  — most recent event is a failure
//   STALE    — never succeeded OR no activity in the last 24h
func verdict(lastOK, lastFail *time.Time, ok24, fail24 int) string {
	if lastFail != nil && (lastOK == nil || lastFail.After(*lastOK)) {
		return "FAILING"
	}
	if ok24 == 0 && fail24 == 0 {
		return "STALE"
	}
	return "OK"
}
