-- migrations/003_insights.sql
-- Sentinel Insights: LLM-generated intelligence from swarm telemetry

BEGIN;

CREATE TABLE IF NOT EXISTS insights (
    id               TEXT PRIMARY KEY,
    timestamp        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    category         TEXT NOT NULL,  -- 'health', 'pattern', 'recommendation', 'anomaly'
    severity         TEXT NOT NULL,  -- 'info', 'warning', 'high', 'critical'
    narrative        TEXT NOT NULL,
    evidence         JSONB,
    suggested_action TEXT,
    scope_type       TEXT,
    scope_value      TEXT,
    acknowledged     BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS idx_insights_recent
    ON insights (timestamp DESC, severity);
CREATE INDEX IF NOT EXISTS idx_insights_scope
    ON insights (scope_type, scope_value, timestamp DESC);

COMMIT;
