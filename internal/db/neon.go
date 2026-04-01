package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EventStore defines the queries Sentinel needs from the telemetry database.
type EventStore interface {
	QueryEvents(ctx context.Context, since, until time.Time) ([]analyzer.Event, error)
	QueryActionCounts(ctx context.Context, since time.Time) ([]ActionCount, error)
	QueryDenialRates(ctx context.Context, since time.Time) ([]DenialRate, error)
	QuerySessionDenials(ctx context.Context, since time.Time) ([]SessionDenialCount, error)
	QueryHourlyVolumes(ctx context.Context, since time.Time) ([]HourlyVolume, error)
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

func (c *NeonClient) QueryEvents(ctx context.Context, since, until time.Time) ([]analyzer.Event, error) {
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

	var events []analyzer.Event
	for rows.Next() {
		var e analyzer.Event
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
