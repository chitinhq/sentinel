package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryStore provides typed SQL queries for the new MCP tools.
type QueryStore struct {
	pool *pgxpool.Pool
}

// NewQueryStore constructs a QueryStore.
func NewQueryStore(pool *pgxpool.Pool) *QueryStore {
	return &QueryStore{pool: pool}
}

// --- sentinel_health ---

// HealthScoreRow is a health score from the database.
type HealthScoreRow struct {
	ScopeValue string         `json:"scope_value"`
	Score      int            `json:"score"`
	Dimensions map[string]int `json:"dimensions"`
	SampleSize int            `json:"sample_size"`
	AsOf       time.Time      `json:"as_of"`
}

// HealthResult is the response shape for sentinel_health.
type HealthResult struct {
	ScopeType string           `json:"scope_type"`
	Scores    []HealthScoreRow `json:"scores"`
}

// LatestHealthScores returns the most recent score per scope value.
func (q *QueryStore) LatestHealthScores(ctx context.Context, scopeType, scopeValue string) (*HealthResult, error) {
	query := `
		SELECT scope_value, score, dimensions, sample_size, timestamp
		FROM health_scores h1
		WHERE scope_type = $1
		  AND timestamp = (
			SELECT MAX(h2.timestamp) FROM health_scores h2
			WHERE h2.scope_type = h1.scope_type
			  AND h2.scope_value = h1.scope_value
		  )
	`
	args := []interface{}{scopeType}
	if scopeValue != "" {
		query += " AND scope_value = $2"
		args = append(args, scopeValue)
	}
	query += " ORDER BY score ASC"

	rows, err := q.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query health scores: %w", err)
	}
	defer rows.Close()

	result := &HealthResult{ScopeType: scopeType}
	for rows.Next() {
		var row HealthScoreRow
		var dimsJSON []byte
		if err := rows.Scan(&row.ScopeValue, &row.Score, &dimsJSON, &row.SampleSize, &row.AsOf); err != nil {
			return nil, fmt.Errorf("scan health score: %w", err)
		}
		if dimsJSON != nil {
			_ = json.Unmarshal(dimsJSON, &row.Dimensions)
		}
		result.Scores = append(result.Scores, row)
	}
	return result, rows.Err()
}

// --- sentinel_query ---

// QueryEventsParams defines filters for event aggregation.
type QueryEventsParams struct {
	Source   string
	Since    time.Duration
	Platform string
	Repo     string
	HasError *bool
}

// FailingCommand is a command with its error count.
type FailingCommand struct {
	Command string `json:"command"`
	Errors  int    `json:"errors"`
	Total   int    `json:"total"`
}

// QueryEventsResult is the response shape for sentinel_query.
type QueryEventsResult struct {
	Since              string           `json:"since"`
	TotalEvents        int              `json:"total_events"`
	ErrorCount         int              `json:"error_count"`
	ErrorRate          float64          `json:"error_rate"`
	TopFailingCommands []FailingCommand `json:"top_failing_commands"`
}

// QueryEvents returns aggregated stats from execution_events.
func (q *QueryStore) QueryEvents(ctx context.Context, params QueryEventsParams) (*QueryEventsResult, error) {
	since := time.Now().Add(-params.Since)

	// Build WHERE clauses dynamically.
	conditions := []string{"timestamp >= $1"}
	args := []interface{}{since}
	argIdx := 2

	if params.Source != "" && params.Source != "all" {
		conditions = append(conditions, fmt.Sprintf("source = $%d", argIdx))
		args = append(args, params.Source)
		argIdx++
	}
	if params.Platform != "" {
		conditions = append(conditions, fmt.Sprintf("agent_id = $%d", argIdx))
		args = append(args, params.Platform)
		argIdx++
	}
	if params.Repo != "" {
		conditions = append(conditions, fmt.Sprintf("repository = $%d", argIdx))
		args = append(args, params.Repo)
		argIdx++
	}
	if params.HasError != nil {
		conditions = append(conditions, fmt.Sprintf("(has_error OR exit_code != 0) = $%d", argIdx))
		args = append(args, *params.HasError)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	// Total + error count.
	var total, errors int
	err := q.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE has_error OR (exit_code IS NOT NULL AND exit_code != 0))::int
		FROM execution_events
		WHERE %s
	`, where), args...).Scan(&total, &errors)
	if err != nil {
		return nil, fmt.Errorf("query totals: %w", err)
	}

	// Top failing commands.
	rows, err := q.pool.Query(ctx, fmt.Sprintf(`
		SELECT command,
			COUNT(*) FILTER (WHERE has_error OR (exit_code IS NOT NULL AND exit_code != 0))::int AS cmd_errors,
			COUNT(*)::int AS cmd_total
		FROM execution_events
		WHERE %s
		GROUP BY command
		HAVING COUNT(*) FILTER (WHERE has_error OR (exit_code IS NOT NULL AND exit_code != 0)) > 0
		ORDER BY cmd_errors DESC
		LIMIT 10
	`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("query failing commands: %w", err)
	}
	defer rows.Close()

	var cmds []FailingCommand
	for rows.Next() {
		var fc FailingCommand
		if err := rows.Scan(&fc.Command, &fc.Errors, &fc.Total); err != nil {
			return nil, fmt.Errorf("scan failing command: %w", err)
		}
		cmds = append(cmds, fc)
	}

	errorRate := 0.0
	if total > 0 {
		errorRate = float64(errors) / float64(total)
	}

	return &QueryEventsResult{
		Since:              formatDuration(params.Since),
		TotalEvents:        total,
		ErrorCount:         errors,
		ErrorRate:          errorRate,
		TopFailingCommands: cmds,
	}, nil
}

// --- sentinel_failures ---

// FailureRow is a single failure event.
type FailureRow struct {
	Timestamp  time.Time `json:"timestamp"`
	Source     string    `json:"source"`
	Platform   string    `json:"platform"`
	Command    string    `json:"command"`
	Repository string    `json:"repository"`
	ExitCode   int       `json:"exit_code"`
	Reason     string    `json:"reason,omitempty"`
}

// FailuresResult is the response for sentinel_failures.
type FailuresResult struct {
	Failures []FailureRow `json:"failures"`
}

// RecentFailures returns individual failure events.
func (q *QueryStore) RecentFailures(ctx context.Context, since time.Time, platform string, limit int) (*FailuresResult, error) {
	query := `
		SELECT timestamp, source, COALESCE(agent_id, ''), command, COALESCE(repository, ''),
		       COALESCE(exit_code, 1), COALESCE(tags->>'reason', '')
		FROM execution_events
		WHERE timestamp >= $1
		  AND (has_error OR (exit_code IS NOT NULL AND exit_code != 0))
	`
	args := []interface{}{since}
	if platform != "" {
		query += " AND agent_id = $2"
		args = append(args, platform)
	}
	query += " ORDER BY timestamp DESC LIMIT " + strconv.Itoa(limit)

	rows, err := q.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query failures: %w", err)
	}
	defer rows.Close()

	result := &FailuresResult{}
	for rows.Next() {
		var f FailureRow
		if err := rows.Scan(&f.Timestamp, &f.Source, &f.Platform, &f.Command, &f.Repository, &f.ExitCode, &f.Reason); err != nil {
			return nil, fmt.Errorf("scan failure: %w", err)
		}
		result.Failures = append(result.Failures, f)
	}
	return result, rows.Err()
}

// --- sentinel_trends ---

// TrendResult is the response for sentinel_trends.
type TrendResult struct {
	Metric    string  `json:"metric"`
	Scope     string  `json:"scope"`
	Window    string  `json:"window"`
	Current   float64 `json:"current"`
	Baseline  float64 `json:"baseline_7d"`
	Trend     string  `json:"trend"` // "improving", "declining", "stable"
	ChangePct float64 `json:"change_pct"`
}

// ComputeTrend compares a metric in the current window vs 7-day baseline.
func (q *QueryStore) ComputeTrend(ctx context.Context, metric, scopeType, scopeValue, window string) (*TrendResult, error) {
	windowDur := parseDuration(window)
	now := time.Now()
	windowStart := now.Add(-windowDur)
	baselineStart := now.Add(-7 * 24 * time.Hour)

	scopeFilter := ""
	var args []interface{}
	argIdx := 1

	// Build scope filter.
	switch scopeType {
	case "platform":
		scopeFilter = fmt.Sprintf("agent_id = $%d", argIdx)
		args = append(args, scopeValue)
		argIdx++
	case "repo":
		scopeFilter = fmt.Sprintf("repository = $%d", argIdx)
		args = append(args, scopeValue)
		argIdx++
	case "queue":
		scopeFilter = fmt.Sprintf("tags->>'queue' = $%d", argIdx)
		args = append(args, scopeValue)
		argIdx++
	}

	var currentVal, baselineVal float64

	switch metric {
	case "success_rate":
		currentVal = q.querySuccessRate(ctx, scopeFilter, args, windowStart, now)
		baselineVal = q.querySuccessRate(ctx, scopeFilter, args, baselineStart, windowStart)
	case "latency":
		currentVal = q.queryAvgLatency(ctx, scopeFilter, args, windowStart, now)
		baselineVal = q.queryAvgLatency(ctx, scopeFilter, args, baselineStart, windowStart)
	case "volume":
		currentVal = q.queryVolume(ctx, scopeFilter, args, windowStart, now)
		baselineVal = q.queryVolume(ctx, scopeFilter, args, baselineStart, windowStart)
		// Normalize baseline to same window size.
		if windowDur < 7*24*time.Hour {
			baselineVal = baselineVal * float64(windowDur) / float64(7*24*time.Hour-windowDur)
		}
	case "denial_rate":
		currentVal = q.queryDenialRate(ctx, scopeFilter, args, windowStart, now)
		baselineVal = q.queryDenialRate(ctx, scopeFilter, args, baselineStart, windowStart)
	default:
		return nil, fmt.Errorf("unknown metric: %s", metric)
	}

	changePct := 0.0
	if baselineVal > 0 {
		changePct = ((currentVal - baselineVal) / baselineVal) * 100
	}

	trend := "stable"
	if changePct > 2 {
		trend = "improving"
	} else if changePct < -2 {
		trend = "declining"
	}
	// For latency and denial_rate, direction is inverted.
	if metric == "latency" || metric == "denial_rate" {
		if changePct > 2 {
			trend = "declining"
		} else if changePct < -2 {
			trend = "improving"
		}
	}

	return &TrendResult{
		Metric:    metric,
		Scope:     scopeType + ":" + scopeValue,
		Window:    window,
		Current:   currentVal,
		Baseline:  baselineVal,
		Trend:     trend,
		ChangePct: changePct,
	}, nil
}

func (q *QueryStore) querySuccessRate(ctx context.Context, scopeFilter string, scopeArgs []interface{}, since, until time.Time) float64 {
	where := "timestamp >= $1 AND timestamp < $2"
	args := []interface{}{since, until}
	if scopeFilter != "" {
		// Renumber scope args.
		for i, a := range scopeArgs {
			_ = i
			where += " AND " + renumberArg(scopeFilter, len(args)+1)
			args = append(args, a)
			break // only one scope filter
		}
	}
	var total, successes int
	q.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE NOT has_error AND (exit_code IS NULL OR exit_code = 0))::int
		FROM execution_events WHERE %s
	`, where), args...).Scan(&total, &successes)
	if total == 0 {
		return 0
	}
	return float64(successes) / float64(total)
}

func (q *QueryStore) queryAvgLatency(ctx context.Context, scopeFilter string, scopeArgs []interface{}, since, until time.Time) float64 {
	where := "timestamp >= $1 AND timestamp < $2 AND duration_ms IS NOT NULL"
	args := []interface{}{since, until}
	if scopeFilter != "" {
		for _, a := range scopeArgs {
			where += " AND " + renumberArg(scopeFilter, len(args)+1)
			args = append(args, a)
			break
		}
	}
	var avg float64
	q.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(AVG(duration_ms)::float, 0)
		FROM execution_events WHERE %s
	`, where), args...).Scan(&avg)
	return avg
}

func (q *QueryStore) queryVolume(ctx context.Context, scopeFilter string, scopeArgs []interface{}, since, until time.Time) float64 {
	where := "timestamp >= $1 AND timestamp < $2"
	args := []interface{}{since, until}
	if scopeFilter != "" {
		for _, a := range scopeArgs {
			where += " AND " + renumberArg(scopeFilter, len(args)+1)
			args = append(args, a)
			break
		}
	}
	var count int
	q.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*)::int FROM execution_events WHERE %s
	`, where), args...).Scan(&count)
	return float64(count)
}

func (q *QueryStore) queryDenialRate(ctx context.Context, scopeFilter string, scopeArgs []interface{}, since, until time.Time) float64 {
	where := "timestamp >= $1 AND timestamp < $2 AND source = 'chitin_governance'"
	args := []interface{}{since, until}
	if scopeFilter != "" {
		for _, a := range scopeArgs {
			where += " AND " + renumberArg(scopeFilter, len(args)+1)
			args = append(args, a)
			break
		}
	}
	var total, denials int
	q.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE has_error)::int
		FROM execution_events WHERE %s
	`, where), args...).Scan(&total, &denials)
	if total == 0 {
		return 0
	}
	return float64(denials) / float64(total)
}

// renumberArg replaces $1 in a filter string with $N.
func renumberArg(filter string, n int) string {
	return strings.Replace(filter, "$1", fmt.Sprintf("$%d", n), 1)
}

// --- sentinel_insights (DB) ---

// InsightRow is an insight from the database.
type InsightRow struct {
	ID              string         `json:"id"`
	Timestamp       time.Time      `json:"timestamp"`
	Category        string         `json:"category"`
	Severity        string         `json:"severity"`
	Narrative       string         `json:"narrative"`
	Evidence        map[string]any `json:"evidence,omitempty"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	ScopeType       string         `json:"scope_type,omitempty"`
	ScopeValue      string         `json:"scope_value,omitempty"`
	Acknowledged    bool           `json:"acknowledged"`
}

// QueryInsights returns recent insights from the DB with optional filters.
func (q *QueryStore) QueryInsights(ctx context.Context, category, minSeverity string, limit int) ([]InsightRow, error) {
	conditions := []string{"1=1"}
	args := []interface{}{}
	argIdx := 1

	if category != "" {
		conditions = append(conditions, fmt.Sprintf("category = $%d", argIdx))
		args = append(args, category)
		argIdx++
	}

	if minSeverity != "" {
		sevOrder := map[string]int{"info": 0, "warning": 1, "high": 2, "critical": 3}
		minOrd, ok := sevOrder[minSeverity]
		if ok {
			var sevValues []string
			for sev, ord := range sevOrder {
				if ord >= minOrd {
					sevValues = append(sevValues, sev)
				}
			}
			conditions = append(conditions, fmt.Sprintf("severity = ANY($%d)", argIdx))
			args = append(args, sevValues)
			argIdx++
		}
	}

	where := strings.Join(conditions, " AND ")
	query := fmt.Sprintf(`
		SELECT id, timestamp, category, severity, narrative,
		       COALESCE(evidence, '{}'), COALESCE(suggested_action, ''),
		       COALESCE(scope_type, ''), COALESCE(scope_value, ''), acknowledged
		FROM insights
		WHERE %s
		ORDER BY timestamp DESC
		LIMIT %d
	`, where, limit)

	rows, err := q.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query insights: %w", err)
	}
	defer rows.Close()

	var results []InsightRow
	for rows.Next() {
		var r InsightRow
		var evidenceJSON []byte
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.Category, &r.Severity,
			&r.Narrative, &evidenceJSON, &r.SuggestedAction,
			&r.ScopeType, &r.ScopeValue, &r.Acknowledged); err != nil {
			return nil, fmt.Errorf("scan insight: %w", err)
		}
		if evidenceJSON != nil {
			_ = json.Unmarshal(evidenceJSON, &r.Evidence)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// parseDuration parses "24h" or "7d" into time.Duration.
func parseDuration(s string) time.Duration {
	if strings.HasSuffix(s, "d") {
		days, _ := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return time.Duration(days) * 24 * time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 24 * time.Hour // default
	}
	return d
}

// formatDuration formats a duration as "24h" or "7d".
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	if hours >= 24 && hours%24 == 0 {
		return fmt.Sprintf("%dd", hours/24)
	}
	return fmt.Sprintf("%dh", hours)
}
