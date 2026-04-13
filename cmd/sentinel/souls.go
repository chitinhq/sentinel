package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/jackc/pgx/v5/pgxpool"
)

// soulRow is one row of the souls scorecard: a (soul, stage) pair with
// the axes we can derive from governance_events alone. Ship velocity
// and polish rate require PR metadata we don't yet join — see issue #49.
type soulRow struct {
	Soul            string
	Stage           string
	Sessions        int
	Events          int
	AllowCount      int
	RatingSum       float64
	RatingSamples   int
	SentinelEvents  int // action LIKE 'sentinel.%' within the pair — per-session denominator for findings/sess
}

// runSouls prints a scorecard of (soul, stage) performance pulled
// straight from governance_events. No new tables; we lean on
// metadata->>'soul' and metadata->>'observed_stage' jsonb extractors,
// same pattern drivers.go uses for host/model.
//
// When no rows come back (chitin#94 hasn't landed or no sessions have
// been stamped yet), we print a hint pointing at the upstream work so
// the operator knows *why* the scorecard is empty.
func runSouls() error {
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

	rows, err := querySouls(ctx, pool)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SOUL\tSTAGE\tSESSIONS\tSAFETY\tRATING\tFINDINGS/SESS")

	if len(rows) == 0 {
		fmt.Fprintln(w, "(no soul-stamped events — waiting on chitinhq/chitin#94 + #95)\t\t\t\t\t")
		if err := w.Flush(); err != nil {
			return err
		}
		return nil
	}

	for _, r := range rows {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
			dash(r.Soul),
			dash(r.Stage),
			r.Sessions,
			fmtSafety(r.AllowCount, r.Events),
			fmtRating(r.RatingSum, r.RatingSamples),
			fmtFindingsPerSession(r.SentinelEvents, r.Sessions),
		)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("(ship_velocity / polish_rate require PR metadata — see issue #49 followups)")
	return nil
}

// soulsQuery is the governance_events rollup for the scorecard.
//
// Notes:
//   - metadata->>'soul' and metadata->>'observed_stage' mirror the
//     host/model extractors in queryDrivers.
//   - FINDINGS/SESS is approximated by "sentinel.*" actions within the
//     same (soul, stage) window. It's not the same as the analyzer's
//     own finding output but it's the same signal the digest uses.
//   - We filter out empty souls server-side to keep the result set small.
const soulsQuery = `
SELECT
  COALESCE(metadata->>'soul', '')           AS soul,
  COALESCE(metadata->>'observed_stage', '') AS stage,
  COUNT(DISTINCT session_id)                AS sessions,
  COUNT(*)                                  AS events,
  COUNT(*) FILTER (WHERE outcome = 'allow') AS allow_count,
  COALESCE(SUM(NULLIF(metadata->>'rating','')::float), 0)  AS rating_sum,
  COUNT(*) FILTER (WHERE metadata ? 'rating')              AS rating_samples,
  COUNT(*) FILTER (WHERE action LIKE 'sentinel.%%')        AS sentinel_events
FROM governance_events
WHERE metadata ? 'soul' AND metadata->>'soul' <> ''
GROUP BY soul, stage
ORDER BY sessions DESC, soul, stage
`

func querySouls(ctx context.Context, pool *pgxpool.Pool) ([]soulRow, error) {
	rows, err := pool.Query(ctx, soulsQuery)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []soulRow
	for rows.Next() {
		var r soulRow
		if err := rows.Scan(
			&r.Soul, &r.Stage, &r.Sessions, &r.Events,
			&r.AllowCount, &r.RatingSum, &r.RatingSamples, &r.SentinelEvents,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter: %w", err)
	}
	return out, nil
}

// fmtSafety renders allow/total as a 0.00–1.00 rate. When total is 0
// we return "—" instead of NaN so the table stays readable.
func fmtSafety(allow, total int) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.2f", float64(allow)/float64(total))
}

// fmtRating returns the mean of metadata.rating samples, or "—" when
// no events carried a rating key (the common case pre-chitin#95).
func fmtRating(sum float64, samples int) string {
	if samples <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", sum/float64(samples))
}

// fmtFindingsPerSession is sentinel.* actions per distinct session.
// "—" when we saw zero sessions (shouldn't happen given the query
// filter, but we guard anyway for divide-by-zero hygiene).
func fmtFindingsPerSession(findings, sessions int) string {
	if sessions <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f", float64(findings)/float64(sessions))
}
