package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Store persists insights to Neon, Redis, and ntfy.
type Store struct {
	pool      *pgxpool.Pool
	redis     *redis.Client
	ntfyTopic string // e.g. "ganglia"
}

// NewStore constructs a Store.
func NewStore(pool *pgxpool.Pool, redisClient *redis.Client, ntfyTopic string) *Store {
	return &Store{pool: pool, redis: redisClient, ntfyTopic: ntfyTopic}
}

// Write persists insights to all destinations.
func (s *Store) Write(ctx context.Context, insights []Insight) error {
	if len(insights) == 0 {
		return nil
	}

	// 1. Neon
	if err := s.writeNeon(ctx, insights); err != nil {
		return fmt.Errorf("neon write: %w", err)
	}

	// 2. Redis
	if s.redis != nil {
		s.writeRedis(ctx, insights)
	}

	// 3. ntfy for high/critical
	for _, ins := range insights {
		if ins.Severity == SeverityHigh || ins.Severity == SeverityCritical {
			s.notifyNtfy(ctx, ins)
		}
	}

	return nil
}

func (s *Store) writeNeon(ctx context.Context, insights []Insight) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, ins := range insights {
		evidenceJSON, _ := json.Marshal(ins.Evidence)
		_, err := tx.Exec(ctx, `
			INSERT INTO insights (id, timestamp, category, severity, narrative, evidence, suggested_action, scope_type, scope_value, acknowledged)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, FALSE)
			ON CONFLICT (id) DO NOTHING
		`, ins.ID, ins.Timestamp, string(ins.Category), string(ins.Severity),
			ins.Narrative, evidenceJSON, ins.SuggestedAction,
			ins.ScopeType, ins.ScopeValue)
		if err != nil {
			return fmt.Errorf("insert insight %s: %w", ins.ID, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) writeRedis(ctx context.Context, insights []Insight) {
	// Cache latest insights as JSON array with 2h TTL.
	data, err := json.Marshal(insights)
	if err != nil {
		return
	}
	s.redis.Set(ctx, "octi:insights:latest", string(data), 2*time.Hour)

	// Count unacknowledged high/critical.
	highCount := 0
	for _, ins := range insights {
		if ins.Severity == SeverityHigh || ins.Severity == SeverityCritical {
			highCount++
		}
	}
	if highCount > 0 {
		s.redis.IncrBy(ctx, "octi:insights:unacked", int64(highCount))
		s.redis.Expire(ctx, "octi:insights:unacked", 24*time.Hour)
	}
}

func (s *Store) notifyNtfy(ctx context.Context, ins Insight) {
	if s.ntfyTopic == "" {
		return
	}
	url := fmt.Sprintf("https://ntfy.sh/%s", s.ntfyTopic)
	title := fmt.Sprintf("[sentinel] %s %s", ins.Severity, ins.Category)
	body := ins.Narrative
	if ins.SuggestedAction != "" {
		body += "\n\nAction: " + ins.SuggestedAction
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", ntfyPriority(ins.Severity))

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func ntfyPriority(sev InsightSeverity) string {
	switch sev {
	case SeverityCritical:
		return "urgent"
	case SeverityHigh:
		return "high"
	case SeverityWarning:
		return "default"
	default:
		return "low"
	}
}
