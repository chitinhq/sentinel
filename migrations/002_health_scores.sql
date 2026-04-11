-- migrations/002_health_scores.sql
-- Sentinel Observability: Health scores for platform/repo/queue monitoring

BEGIN;

CREATE TABLE IF NOT EXISTS health_scores (
    id           SERIAL PRIMARY KEY,
    timestamp    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    scope_type   TEXT NOT NULL,  -- 'platform', 'repo', 'queue'
    scope_value  TEXT NOT NULL,  -- 'claude', 'chitinhq/octi', 'build'
    score        INTEGER NOT NULL CHECK (score >= 0 AND score <= 100),
    dimensions   JSONB NOT NULL, -- per-dimension breakdown
    sample_size  INTEGER NOT NULL,
    window_hours INTEGER NOT NULL DEFAULT 24
);

CREATE INDEX IF NOT EXISTS idx_health_latest
    ON health_scores (scope_type, scope_value, timestamp DESC);

COMMIT;
