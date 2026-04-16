// Package circuit — neon_source.go is the production SignalSource
// implementation. It reads execution_events for retry/resource/
// telemetry-coverage signals and shells out to `gh api` for the
// per-repo health signal (open PR count + recent CI failure rate).
//
// Notes on column choices (sentinel's execution_events has no
// task_id column — see migrations/001):
//
//   - retry_storm uses (command, repository) as the task-identity
//     proxy. The same command run repeatedly against the same repo
//     within the window is the closest unambiguous "retry" signal
//     available without re-keying the schema.
//   - resource_burn counts distinct session_ids active in the last
//     hour as "active agents", uses the longest-running such session
//     as oldest, and approximates token burn from arguments JSON
//     (sum of fields["tokens"] when present) divided by elapsed
//     minutes.
//   - telemetry_integrity samples recent rows and computes the
//     fraction with all of (agent_id, session_id, command) populated.
//   - repo_health is gh-backed because PR counts and CI failure rate
//     live on GitHub, not in execution_events.
package circuit

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NeonSignalSource implements SignalSource against a Neon Postgres pool
// and the gh CLI. Repos is the list of repositories to query for the
// repo-health signal (e.g. {"chitinhq/octi", "chitinhq/clawta"}).
type NeonSignalSource struct {
	Pool  *pgxpool.Pool
	Repos []string

	// GhRunner is overridable for tests; defaults to runGh which shells
	// out to the real `gh` CLI.
	GhRunner func(ctx context.Context, args ...string) ([]byte, error)
}

// NewNeonSignalSource returns a source bound to pool with default gh
// runner.
func NewNeonSignalSource(pool *pgxpool.Pool, repos []string) *NeonSignalSource {
	return &NeonSignalSource{Pool: pool, Repos: repos, GhRunner: runGh}
}

// RetryCounts groups recent events by (command, repository) and returns
// counts per synthetic task_id "command|repository".
func (s *NeonSignalSource) RetryCounts(ctx context.Context, window time.Duration) (map[string]int, error) {
	since := time.Now().UTC().Add(-window)
	rows, err := s.Pool.Query(ctx, `
		SELECT command, COALESCE(repository, ''), COUNT(*) AS n
		FROM execution_events
		WHERE timestamp >= $1
		GROUP BY command, repository
		HAVING COUNT(*) > 1
	`, since)
	if err != nil {
		return nil, fmt.Errorf("retry query: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var cmd, repo string
		var n int
		if err := rows.Scan(&cmd, &repo, &n); err != nil {
			return nil, err
		}
		out[cmd+"|"+repo] = n
	}
	return out, rows.Err()
}

// ActiveAgents counts distinct session_ids with at least one event in
// the last hour, returns the age of the oldest such session, and a
// rough tokens/min estimate from arguments JSON.
func (s *NeonSignalSource) ActiveAgents(ctx context.Context) (int, time.Duration, float64, error) {
	since := time.Now().UTC().Add(-1 * time.Hour)
	row := s.Pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT session_id),
		       COALESCE(MIN(timestamp), NOW())
		FROM execution_events
		WHERE timestamp >= $1
	`, since)
	var count int
	var oldestTs time.Time
	if err := row.Scan(&count, &oldestTs); err != nil {
		return 0, 0, 0, fmt.Errorf("active-agents query: %w", err)
	}
	oldest := time.Since(oldestTs)
	if count == 0 {
		oldest = 0
	}

	// Token burn rate: sum tokens fields from arguments JSON over a 5-minute
	// window. Best-effort — many event sources don't carry token counts.
	burnSince := time.Now().UTC().Add(-5 * time.Minute)
	rows, err := s.Pool.Query(ctx, `
		SELECT arguments
		FROM execution_events
		WHERE timestamp >= $1 AND arguments IS NOT NULL
	`, burnSince)
	if err != nil {
		return count, oldest, 0, nil
	}
	defer rows.Close()
	var totalTokens float64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		totalTokens += extractTokens(raw)
	}
	tpm := totalTokens / 5.0
	return count, oldest, tpm, nil
}

func extractTokens(raw []byte) float64 {
	if len(raw) == 0 {
		return 0
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0
	}
	for _, k := range []string{"tokens", "total_tokens", "input_tokens", "output_tokens"} {
		if v, ok := m[k]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			}
		}
	}
	return 0
}

// RepoHealth shells out to gh for open PR count and uses execution_events
// has_error rate as a CI-failure proxy per repo (the GH Actions ingester
// writes one event per workflow run, has_error=true on failure).
func (s *NeonSignalSource) RepoHealth(ctx context.Context, window time.Duration) (map[string]RepoStats, error) {
	out := make(map[string]RepoStats, len(s.Repos))
	since := time.Now().UTC().Add(-window)

	for _, repo := range s.Repos {
		stats := RepoStats{}

		// Open PR count via gh api.
		body, err := s.GhRunner(ctx, "api",
			fmt.Sprintf("repos/%s/pulls?state=open&per_page=100", repo),
			"--jq", "length")
		if err == nil {
			var n int
			fmt.Sscanf(strings.TrimSpace(string(body)), "%d", &n)
			stats.OpenPRs = n
		}

		// CI failure rate from execution_events.
		row := s.Pool.QueryRow(ctx, `
			SELECT COUNT(*) FILTER (WHERE has_error) AS failed,
			       COUNT(*) AS total
			FROM execution_events
			WHERE timestamp >= $1 AND repository = $2
		`, since, repo)
		var failed, total int
		if err := row.Scan(&failed, &total); err == nil && total > 0 {
			stats.CIFailureRate = float64(failed) / float64(total)
		}
		out[repo] = stats
	}
	return out, nil
}

// TelemetryCoverage computes the fraction of recent events that have
// the three core required fields populated.
func (s *NeonSignalSource) TelemetryCoverage(ctx context.Context, window time.Duration) (float64, int, error) {
	since := time.Now().UTC().Add(-window)
	row := s.Pool.QueryRow(ctx, `
		SELECT
		  COUNT(*) FILTER (
		    WHERE agent_id IS NOT NULL AND agent_id <> ''
		      AND session_id IS NOT NULL AND session_id <> ''
		      AND command IS NOT NULL AND command <> ''
		  ) AS complete,
		  COUNT(*) AS total
		FROM execution_events
		WHERE timestamp >= $1
	`, since)
	var complete, total int
	if err := row.Scan(&complete, &total); err != nil {
		return 0, 0, fmt.Errorf("telemetry coverage: %w", err)
	}
	if total == 0 {
		return 1.0, 0, nil
	}
	return float64(complete) / float64(total), total, nil
}

// runGh shells out to the gh CLI. Kept as a package-level var so tests
// can override.
func runGh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	return cmd.Output()
}
