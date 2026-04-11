package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chitinhq/sentinel/internal/ingestion"
)

// EventStore defines the queries Sentinel needs from the telemetry database.
type EventStore interface {
	QueryEvents(ctx context.Context, since, until time.Time) ([]Event, error)
	QueryActionCounts(ctx context.Context, since time.Time) ([]ActionCount, error)
	QueryDenialRates(ctx context.Context, since time.Time) ([]DenialRate, error)
	QuerySessionDenials(ctx context.Context, since time.Time) ([]SessionDenialCount, error)
	QueryHourlyVolumes(ctx context.Context, since time.Time) ([]HourlyVolume, error)
	QueryCommandFailureRates(ctx context.Context, since time.Time) ([]CommandFailureRate, error)
	QuerySessionSequences(ctx context.Context, since time.Time) ([]SessionSequence, error)
	Close()
}

type ActionCount struct {
	Action  string
	Outcome string
	Count   int
}

type DenialRate struct {
	Action      string
	TotalCount  int
	DenialCount int
	DenialRate  float64
}

type SessionDenialCount struct {
	SessionID string
	AgentID   string
	Denials   int
	Total     int
}

type HourlyVolume struct {
	Hour  time.Time
	Count int
}

type NeonClient struct {
	pool *pgxpool.Pool
}

func NewNeonClient(ctx context.Context, connURL string) (*NeonClient, error) {
	pool, err := pgxpool.New(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("connect to neon: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping neon: %w", err)
	}
	return &NeonClient{pool: pool}, nil
}

func (c *NeonClient) Close() {
	c.pool.Close()
}

// Pool returns the underlying pgxpool.Pool for direct use by other packages.
func (c *NeonClient) Pool() *pgxpool.Pool {
	return c.pool
}

func (c *NeonClient) QueryEvents(ctx context.Context, since, until time.Time) ([]Event, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT id::text, timestamp, agent_id, session_id, event_type,
		       action, COALESCE(resource, ''), COALESCE(outcome, ''),
		       COALESCE(risk_level, 'low'), COALESCE(policy_version, ''),
		       COALESCE(metadata::text, '{}')
		FROM governance_events
		WHERE timestamp >= $1 AND timestamp < $2
		ORDER BY timestamp ASC
	`, since, until)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var metaJSON string
		err := rows.Scan(&e.ID, &e.Timestamp, &e.AgentID, &e.SessionID,
			&e.EventType, &e.Action, &e.Resource, &e.Outcome,
			&e.RiskLevel, &e.PolicyVersion, &metaJSON)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		if metaJSON != "" {
			_ = json.Unmarshal([]byte(metaJSON), &e.Metadata)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (c *NeonClient) QueryActionCounts(ctx context.Context, since time.Time) ([]ActionCount, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT action, COALESCE(outcome, 'unknown'), COUNT(*)::int
		FROM governance_events
		WHERE timestamp >= $1
		GROUP BY action, outcome
		ORDER BY COUNT(*) DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query action counts: %w", err)
	}
	defer rows.Close()

	var counts []ActionCount
	for rows.Next() {
		var ac ActionCount
		if err := rows.Scan(&ac.Action, &ac.Outcome, &ac.Count); err != nil {
			return nil, fmt.Errorf("scan action count: %w", err)
		}
		counts = append(counts, ac)
	}
	return counts, rows.Err()
}

func (c *NeonClient) QueryDenialRates(ctx context.Context, since time.Time) ([]DenialRate, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT action,
		       COUNT(*)::int AS total,
		       COUNT(*) FILTER (WHERE outcome = 'deny')::int AS denials,
		       CASE WHEN COUNT(*) > 0
		            THEN COUNT(*) FILTER (WHERE outcome = 'deny')::float / COUNT(*)
		            ELSE 0 END AS rate
		FROM governance_events
		WHERE timestamp >= $1
		GROUP BY action
		HAVING COUNT(*) > 0
		ORDER BY rate DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query denial rates: %w", err)
	}
	defer rows.Close()

	var rates []DenialRate
	for rows.Next() {
		var dr DenialRate
		if err := rows.Scan(&dr.Action, &dr.TotalCount, &dr.DenialCount, &dr.DenialRate); err != nil {
			return nil, fmt.Errorf("scan denial rate: %w", err)
		}
		rates = append(rates, dr)
	}
	return rates, rows.Err()
}

func (c *NeonClient) QuerySessionDenials(ctx context.Context, since time.Time) ([]SessionDenialCount, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT session_id, agent_id,
		       COUNT(*) FILTER (WHERE outcome = 'deny')::int AS denials,
		       COUNT(*)::int AS total
		FROM governance_events
		WHERE timestamp >= $1
		GROUP BY session_id, agent_id
		HAVING COUNT(*) FILTER (WHERE outcome = 'deny') > 0
		ORDER BY denials DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query session denials: %w", err)
	}
	defer rows.Close()

	var counts []SessionDenialCount
	for rows.Next() {
		var sc SessionDenialCount
		if err := rows.Scan(&sc.SessionID, &sc.AgentID, &sc.Denials, &sc.Total); err != nil {
			return nil, fmt.Errorf("scan session denial: %w", err)
		}
		counts = append(counts, sc)
	}
	return counts, rows.Err()
}

func (c *NeonClient) QueryHourlyVolumes(ctx context.Context, since time.Time) ([]HourlyVolume, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT date_trunc('hour', timestamp) AS hour, COUNT(*)::int
		FROM governance_events
		WHERE timestamp >= $1
		GROUP BY hour
		ORDER BY hour ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query hourly volumes: %w", err)
	}
	defer rows.Close()

	var vols []HourlyVolume
	for rows.Next() {
		var hv HourlyVolume
		if err := rows.Scan(&hv.Hour, &hv.Count); err != nil {
			return nil, fmt.Errorf("scan hourly volume: %w", err)
		}
		vols = append(vols, hv)
	}
	return vols, rows.Err()
}

// InsertExecutionEvents batch-inserts execution events using a transaction.
// Conflicts on the primary key are silently ignored (ON CONFLICT DO NOTHING).
// Returns the number of rows actually inserted.
func (c *NeonClient) InsertExecutionEvents(ctx context.Context, events []ingestion.ExecutionEvent) (int, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	inserted := 0
	for _, e := range events {
		argsJSON, err := json.Marshal(e.Arguments)
		if err != nil {
			return inserted, fmt.Errorf("marshal arguments: %w", err)
		}
		tagsJSON, err := json.Marshal(e.Tags)
		if err != nil {
			return inserted, fmt.Errorf("marshal tags: %w", err)
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO execution_events (
				id, timestamp, source, session_id, sequence_num,
				actor, agent_id, command, arguments,
				exit_code, duration_ms, working_dir,
				repository, branch, stdout_hash, stderr_hash,
				has_error, tags
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10, $11, $12,
				$13, $14, $15, $16,
				$17, $18
			) ON CONFLICT (id) DO NOTHING
		`,
			e.ID, e.Timestamp, string(e.Source), e.SessionID, e.SequenceNum,
			string(e.Actor), e.AgentID, e.Command, argsJSON,
			e.ExitCode, e.DurationMs, e.WorkingDir,
			e.Repository, e.Branch, e.StdoutHash, e.StderrHash,
			e.HasError, tagsJSON,
		)
		if err != nil {
			return inserted, fmt.Errorf("insert execution event %s: %w", e.ID, err)
		}
		inserted += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}
	return inserted, nil
}

// GetCheckpoint retrieves the ingestion checkpoint for the given adapter.
// Returns nil (no error) when no checkpoint exists yet.
func (c *NeonClient) GetCheckpoint(ctx context.Context, adapter string) (*ingestion.Checkpoint, error) {
	row := c.pool.QueryRow(ctx, `
		SELECT adapter, COALESCE(last_run_id, ''), COALESCE(last_run_at, '1970-01-01'::timestamptz)
		FROM ingestion_checkpoints
		WHERE adapter = $1
	`, adapter)

	var cp ingestion.Checkpoint
	err := row.Scan(&cp.Adapter, &cp.LastRunID, &cp.LastRunAt)
	if err != nil {
		// pgx returns pgx.ErrNoRows when nothing found; treat as nil checkpoint.
		return nil, nil //nolint:nilerr
	}
	return &cp, nil
}

// UpsertCheckpoint inserts or updates the checkpoint for the given adapter.
func (c *NeonClient) UpsertCheckpoint(ctx context.Context, cp ingestion.Checkpoint) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO ingestion_checkpoints (adapter, last_run_id, last_run_at, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (adapter) DO UPDATE
		  SET last_run_id = EXCLUDED.last_run_id,
		      last_run_at = EXCLUDED.last_run_at,
		      updated_at  = NOW()
	`, cp.Adapter, cp.LastRunID, cp.LastRunAt)
	if err != nil {
		return fmt.Errorf("upsert checkpoint: %w", err)
	}
	return nil
}

// HealthScoreRow represents a health score row from the database.
type HealthScoreRow struct {
	ScopeType   string         `json:"scope_type"`
	ScopeValue  string         `json:"scope_value"`
	Score       int            `json:"score"`
	Dimensions  map[string]int `json:"dimensions"`
	SampleSize  int            `json:"sample_size"`
	Timestamp   time.Time      `json:"timestamp"`
}

// QueryLatestHealthScores retrieves the latest health score per scope.
// If scopeType is empty, returns all scopes. If scopeValue is non-empty,
// filters to that specific value.
func QueryLatestHealthScores(ctx context.Context, pool *pgxpool.Pool, scopeType, scopeValue string) ([]HealthScoreRow, error) {
	query := `
		SELECT scope_type, scope_value, score, dimensions, sample_size, timestamp
		FROM health_scores h1
		WHERE timestamp = (
			SELECT MAX(h2.timestamp) FROM health_scores h2
			WHERE h2.scope_type = h1.scope_type
			  AND h2.scope_value = h1.scope_value
		)
	`
	args := []interface{}{}
	argIdx := 1

	if scopeType != "" {
		query += fmt.Sprintf(" AND scope_type = $%d", argIdx)
		args = append(args, scopeType)
		argIdx++
	}
	if scopeValue != "" {
		query += fmt.Sprintf(" AND scope_value = $%d", argIdx)
		args = append(args, scopeValue)
		argIdx++
	}
	query += " ORDER BY score ASC"
	_ = argIdx

	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query health scores: %w", err)
	}
	defer rows.Close()

	var scores []HealthScoreRow
	for rows.Next() {
		var s HealthScoreRow
		var dimsJSON []byte
		if err := rows.Scan(&s.ScopeType, &s.ScopeValue, &s.Score, &dimsJSON, &s.SampleSize, &s.Timestamp); err != nil {
			return nil, fmt.Errorf("scan health score: %w", err)
		}
		if dimsJSON != nil {
			_ = json.Unmarshal(dimsJSON, &s.Dimensions)
		}
		scores = append(scores, s)
	}
	return scores, rows.Err()
}

// QueryCommandFailureRates returns failure rates per command from execution_events
// since the given time, grouped by command.
func (c *NeonClient) QueryCommandFailureRates(ctx context.Context, since time.Time) ([]CommandFailureRate, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT
			command,
			COUNT(*)::int AS total,
			COUNT(*) FILTER (WHERE has_error OR exit_code != 0)::int AS failures,
			CASE WHEN COUNT(*) > 0
			     THEN COUNT(*) FILTER (WHERE has_error OR exit_code != 0)::float / COUNT(*)
			     ELSE 0 END AS failure_rate,
			ARRAY_AGG(DISTINCT repository) FILTER (WHERE repository IS NOT NULL) AS repos,
			ARRAY_AGG(DISTINCT actor) FILTER (WHERE actor IS NOT NULL)           AS actors
		FROM execution_events
		WHERE timestamp >= $1
		GROUP BY command
		HAVING COUNT(*) > 0
		ORDER BY failure_rate DESC, total DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query command failure rates: %w", err)
	}
	defer rows.Close()

	var rates []CommandFailureRate
	for rows.Next() {
		var cfr CommandFailureRate
		if err := rows.Scan(
			&cfr.Command,
			&cfr.TotalCount,
			&cfr.FailureCount,
			&cfr.FailureRate,
			&cfr.Repos,
			&cfr.Actors,
		); err != nil {
			return nil, fmt.Errorf("scan command failure rate: %w", err)
		}
		rates = append(rates, cfr)
	}
	return rates, rows.Err()
}

// QuerySessionSequences returns ordered command sequences per session since the
// given time.  Each entry preserves the execution order via sequence_num.
func (c *NeonClient) QuerySessionSequences(ctx context.Context, since time.Time) ([]SessionSequence, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT session_id, command, COALESCE(exit_code, 0), has_error
		FROM execution_events
		WHERE timestamp >= $1
		ORDER BY session_id, sequence_num ASC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("query session sequences: %w", err)
	}
	defer rows.Close()

	// Accumulate entries per session in order.
	seqMap := make(map[string]*SessionSequence)
	var order []string // preserve insertion order

	for rows.Next() {
		var (
			sessionID string
			entry     SequenceEntry
		)
		if err := rows.Scan(&sessionID, &entry.Command, &entry.ExitCode, &entry.HasError); err != nil {
			return nil, fmt.Errorf("scan session sequence: %w", err)
		}
		if _, ok := seqMap[sessionID]; !ok {
			seqMap[sessionID] = &SessionSequence{SessionID: sessionID}
			order = append(order, sessionID)
		}
		seqMap[sessionID].Events = append(seqMap[sessionID].Events, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sequences := make([]SessionSequence, 0, len(order))
	for _, id := range order {
		sequences = append(sequences, *seqMap[id])
	}
	return sequences, nil
}
