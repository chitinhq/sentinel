package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultStaleThreshold is how long a driver can go without a heartbeat
// before sentinel flags it as STALE. Chitin driver heartbeats fire once
// a minute, so 120s (2 missed beats) is the sensible default.
const defaultStaleThreshold = 120 * time.Second

// driverRow is the rolled-up view of one (driver, host) pair.
type driverRow struct {
	Driver        string
	Host          string
	LastHeartbeat *time.Time
	UptimeSeconds *int64
	Model         string
}

// runDrivers reports fleet health by rolling up flow.driver.*.heartbeat
// events. Mirrors flows.go: no new schema, just GROUP BY over
// governance_events, printed as a table.
func runDrivers() error {
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

	threshold := defaultStaleThreshold
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--threshold-seconds" && i+1 < len(os.Args) {
			if secs, err := strconv.Atoi(os.Args[i+1]); err == nil {
				threshold = time.Duration(secs) * time.Second
			}
		}
	}

	rows, err := queryDrivers(ctx, pool)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DRIVER\tHOST\tLAST_HEARTBEAT\tUPTIME\tMODEL\tSTATUS")

	now := time.Now()
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no driver heartbeat events found — run `chitin driver heartbeat <name>` to populate)\t\t\t\t\t")
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			dash(r.Driver),
			dash(r.Host),
			formatTs(r.LastHeartbeat),
			fmtUptimePtr(r.UptimeSeconds),
			dash(r.Model),
			driverStatus(r.LastHeartbeat, now, threshold),
		)
	}
	return w.Flush()
}

// driverActionRE extracts the driver name from an action string like
// "flow.driver.octi.heartbeat" → "octi".
var driverActionRE = regexp.MustCompile(`^flow\.driver\.(.+)\.heartbeat$`)

func queryDrivers(ctx context.Context, pool *pgxpool.Pool) ([]driverRow, error) {
	// Uses regexp_replace on action so we collapse the per-driver action
	// stream into one row per (driver, host). metadata is jsonb; the
	// chitin_governance adapter flattens heartbeat fields (host,
	// uptime_seconds, model) into it directly.
	rows, err := pool.Query(ctx, `
		SELECT
		  regexp_replace(action, '^flow\.driver\.(.*)\.heartbeat$', '\1') AS driver,
		  COALESCE(metadata->>'host', '')   AS host,
		  MAX(timestamp)                    AS last_heartbeat,
		  MAX(NULLIF(metadata->>'uptime_seconds', '')::bigint) AS uptime_seconds,
		  MAX(COALESCE(metadata->>'model', '')) AS model
		FROM governance_events
		WHERE action LIKE 'flow.driver.%.heartbeat'
		GROUP BY driver, host
		ORDER BY driver, host
	`)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []driverRow
	for rows.Next() {
		var r driverRow
		var lastHB *time.Time
		var uptime *int64
		if err := rows.Scan(&r.Driver, &r.Host, &lastHB, &uptime, &r.Model); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		r.LastHeartbeat = lastHB
		r.UptimeSeconds = uptime
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter: %w", err)
	}
	return out, nil
}

// driverStatus collapses a driver's liveness into a single label.
//
//	NEVER      — no heartbeats ever (caller holds this; query won't emit it)
//	STALE (N)  — last heartbeat older than threshold; N is human duration
//	OK         — heartbeat within threshold
func driverStatus(lastHB *time.Time, now time.Time, threshold time.Duration) string {
	if lastHB == nil {
		return "NEVER"
	}
	age := now.Sub(*lastHB)
	if age > threshold {
		return fmt.Sprintf("STALE (%s)", fmtAge(age))
	}
	return "OK"
}

// fmtUptime turns a seconds count into a compact human string:
//
//	3720 → "1h2m"
//	2700 → "45m"
//	12   → "12s"
//	0    → "0s"
func fmtUptime(secs int64) string {
	if secs <= 0 {
		return "0s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	switch {
	case h > 0:
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func fmtUptimePtr(secs *int64) string {
	if secs == nil {
		return "—"
	}
	return fmtUptime(*secs)
}

// fmtAge is like fmtUptime but for durations (rounded to seconds) — used
// inside "STALE (15m)" labels.
func fmtAge(d time.Duration) string {
	return fmtUptime(int64(d.Round(time.Second).Seconds()))
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
