package health

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Weights for health score dimensions.
type Weights struct {
	SuccessRate           float64 `yaml:"success_rate"`
	GovernanceCompliance  float64 `yaml:"governance_compliance"`
	Latency               float64 `yaml:"latency"`
	BudgetHealth          float64 `yaml:"budget_health"`
	Stability             float64 `yaml:"stability"`
}

// DefaultWeights returns the spec-defined dimension weights.
func DefaultWeights() Weights {
	return Weights{
		SuccessRate:          0.30,
		GovernanceCompliance: 0.25,
		Latency:              0.15,
		BudgetHealth:         0.15,
		Stability:            0.15,
	}
}

// HealthScore represents a computed health score at a given granularity.
type HealthScore struct {
	ScopeType   string         `json:"scope_type"`
	ScopeValue  string         `json:"scope_value"`
	Score       int            `json:"score"`
	Dimensions  map[string]int `json:"dimensions"`
	SampleSize  int            `json:"sample_size"`
	WindowHours int            `json:"window_hours"`
}

// stats holds aggregated event counts for a single group.
type stats struct {
	total      int
	successes  int
	failures   int
	p95Latency int64
	govTotal   int
	govAllow   int
}

// Scorer computes health scores from execution_events data.
type Scorer struct {
	pool    *pgxpool.Pool
	redis   *redis.Client
	weights Weights
}

// NewScorer constructs a Scorer with the given weights.
func NewScorer(pool *pgxpool.Pool, redisClient *redis.Client, weights Weights) *Scorer {
	return &Scorer{pool: pool, redis: redisClient, weights: weights}
}

// ComputeAll computes health scores at all three granularities.
func (s *Scorer) ComputeAll(ctx context.Context) ([]HealthScore, error) {
	var all []HealthScore

	platforms, err := s.computeByGroup(ctx, "platform", "agent_id")
	if err != nil {
		return nil, fmt.Errorf("platform scores: %w", err)
	}
	all = append(all, platforms...)

	repos, err := s.computeByGroup(ctx, "repo", "repository")
	if err != nil {
		return nil, fmt.Errorf("repo scores: %w", err)
	}
	all = append(all, repos...)

	queues, err := s.computeByGroup(ctx, "queue", "tags->>'queue'")
	if err != nil {
		return nil, fmt.Errorf("queue scores: %w", err)
	}
	all = append(all, queues...)

	return all, nil
}

// computeByGroup computes scores grouped by a column expression.
func (s *Scorer) computeByGroup(ctx context.Context, scopeType, groupExpr string) ([]HealthScore, error) {
	since24h := time.Now().Add(-24 * time.Hour)
	since7d := time.Now().Add(-7 * 24 * time.Hour)

	// Query 24h stats grouped by the expression.
	query := fmt.Sprintf(`
		SELECT
			%s AS group_val,
			COUNT(*)::int AS total,
			COUNT(*) FILTER (WHERE NOT has_error AND (exit_code IS NULL OR exit_code = 0))::int AS successes,
			COUNT(*) FILTER (WHERE has_error OR (exit_code IS NOT NULL AND exit_code != 0))::int AS failures,
			COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE duration_ms IS NOT NULL), 0)::bigint AS p95_latency,
			COUNT(*) FILTER (WHERE source = 'chitin_governance')::int AS gov_total,
			COUNT(*) FILTER (WHERE source = 'chitin_governance' AND NOT has_error)::int AS gov_allow
		FROM execution_events
		WHERE timestamp >= $1
		  AND %s IS NOT NULL
		  AND %s != ''
		GROUP BY %s
	`, groupExpr, groupExpr, groupExpr, groupExpr)

	rows, err := s.pool.Query(ctx, query, since24h)
	if err != nil {
		return nil, fmt.Errorf("query 24h stats: %w", err)
	}
	defer rows.Close()

	groupStats := make(map[string]*stats)
	for rows.Next() {
		var groupVal string
		var st stats
		if err := rows.Scan(&groupVal, &st.total, &st.successes, &st.failures, &st.p95Latency, &st.govTotal, &st.govAllow); err != nil {
			return nil, fmt.Errorf("scan stats: %w", err)
		}
		groupStats[groupVal] = &st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Query 7d baseline for stability.
	stabilityQuery := fmt.Sprintf(`
		SELECT
			%s AS group_val,
			COUNT(*) FILTER (WHERE NOT has_error AND (exit_code IS NULL OR exit_code = 0))::float /
			  GREATEST(COUNT(*)::float, 1) AS success_rate_7d
		FROM execution_events
		WHERE timestamp >= $1 AND timestamp < $2
		  AND %s IS NOT NULL
		  AND %s != ''
		GROUP BY %s
	`, groupExpr, groupExpr, groupExpr, groupExpr)

	baselineRows, err := s.pool.Query(ctx, stabilityQuery, since7d, since24h)
	if err != nil {
		return nil, fmt.Errorf("query 7d baseline: %w", err)
	}
	defer baselineRows.Close()

	baselines := make(map[string]float64)
	for baselineRows.Next() {
		var groupVal string
		var rate float64
		if err := baselineRows.Scan(&groupVal, &rate); err != nil {
			return nil, fmt.Errorf("scan baseline: %w", err)
		}
		baselines[groupVal] = rate
	}

	// Compute scores.
	var scores []HealthScore
	for groupVal, st := range groupStats {
		dims := s.computeDimensions(st, baselines[groupVal])
		composite := s.weightedScore(dims)

		scores = append(scores, HealthScore{
			ScopeType:   scopeType,
			ScopeValue:  groupVal,
			Score:       composite,
			Dimensions:  dims,
			SampleSize:  st.total,
			WindowHours: 24,
		})
	}

	return scores, nil
}

func (s *Scorer) computeDimensions(st *stats, baseline7d float64) map[string]int {
	dims := make(map[string]int)

	// Success rate (0-100).
	if st.total > 0 {
		dims["success_rate"] = clamp(int(float64(st.successes) / float64(st.total) * 100))
	} else {
		dims["success_rate"] = 0
	}

	// Governance compliance (0-100).
	if st.govTotal > 0 {
		dims["governance_compliance"] = clamp(int(float64(st.govAllow) / float64(st.govTotal) * 100))
	} else {
		dims["governance_compliance"] = 50 // no data → neutral
	}

	// Latency score (0-100). Lower p95 = better.
	// Baseline: 60s (60000ms) = 0, 0ms = 100.
	dims["latency"] = latencyScore(st.p95Latency)

	// Budget health — read from Redis if available, else default 50.
	dims["budget_health"] = 50 // overridden by caller if Redis data available

	// Stability — compare current success rate to 7d baseline.
	if st.total > 0 && baseline7d > 0 {
		currentRate := float64(st.successes) / float64(st.total)
		delta := currentRate - baseline7d
		// Map delta to 0-100: -0.5 or worse = 0, +0.5 or better = 100, 0 = 50
		stability := 50 + int(delta*100)
		dims["stability"] = clamp(stability)
	} else {
		dims["stability"] = 50 // no baseline → neutral
	}

	return dims
}

func (s *Scorer) weightedScore(dims map[string]int) int {
	score := float64(dims["success_rate"])*s.weights.SuccessRate +
		float64(dims["governance_compliance"])*s.weights.GovernanceCompliance +
		float64(dims["latency"])*s.weights.Latency +
		float64(dims["budget_health"])*s.weights.BudgetHealth +
		float64(dims["stability"])*s.weights.Stability

	return clamp(int(math.Round(score)))
}

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// latencyScore maps p95 latency to 0-100 score.
// 0ms → 100, 60000ms (60s) → 0. Linear interpolation.
func latencyScore(p95Ms int64) int {
	if p95Ms <= 0 {
		return 100
	}
	const maxMs = 60000
	if p95Ms >= maxMs {
		return 0
	}
	return clamp(int(100 - (p95Ms * 100 / maxMs)))
}

// EnrichBudgetHealth reads budget data from Redis and updates the
// budget_health dimension for platform-scoped scores.
func (s *Scorer) EnrichBudgetHealth(ctx context.Context, scores []HealthScore) {
	if s.redis == nil {
		return
	}
	for i := range scores {
		if scores[i].ScopeType != "platform" {
			continue
		}
		platform := scores[i].ScopeValue
		count, err := s.redis.Get(ctx, "octi:dispatch-count:"+platform).Int()
		if err != nil {
			continue
		}
		capStr, err := s.redis.HGet(ctx, "octi:budget:"+platform, "daily_cap").Result()
		if err != nil {
			continue
		}
		cap := 0
		fmt.Sscanf(capStr, "%d", &cap)
		if cap > 0 {
			remaining := 100 - (count * 100 / cap)
			scores[i].Dimensions["budget_health"] = clamp(remaining)
			// Recompute composite.
			scores[i].Score = s.weightedScore(scores[i].Dimensions)
		}
	}
}

// PersistToNeon writes health scores to the health_scores table.
func (s *Scorer) PersistToNeon(ctx context.Context, scores []HealthScore) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, sc := range scores {
		dimsJSON, err := json.Marshal(sc.Dimensions)
		if err != nil {
			return fmt.Errorf("marshal dimensions: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO health_scores (timestamp, scope_type, scope_value, score, dimensions, sample_size, window_hours)
			VALUES (NOW(), $1, $2, $3, $4, $5, $6)
		`, sc.ScopeType, sc.ScopeValue, sc.Score, dimsJSON, sc.SampleSize, sc.WindowHours)
		if err != nil {
			return fmt.Errorf("insert health score: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// PushToRedis writes health scores to Redis hashes for brain consumption.
func (s *Scorer) PushToRedis(ctx context.Context, scores []HealthScore) error {
	if s.redis == nil {
		return nil
	}
	pipe := s.redis.Pipeline()
	for _, sc := range scores {
		key := fmt.Sprintf("octi:health:%s", sc.ScopeValue)
		dimsJSON, _ := json.Marshal(sc.Dimensions)
		pipe.HSet(ctx, key, map[string]interface{}{
			"score":      sc.Score,
			"updated_at": time.Now().UTC().Format(time.RFC3339),
			"dimensions": string(dimsJSON),
			"scope_type": sc.ScopeType,
		})
		pipe.Expire(ctx, key, 1*time.Hour)
	}
	_, err := pipe.Exec(ctx)
	return err
}
