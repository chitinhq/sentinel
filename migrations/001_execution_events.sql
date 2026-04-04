-- migrations/001_execution_events.sql
-- Execution Telemetry Mining Phase 1
-- Run against Neon Postgres

BEGIN;

CREATE TABLE IF NOT EXISTS execution_events (
    id              TEXT PRIMARY KEY,
    timestamp       TIMESTAMPTZ NOT NULL,
    source          TEXT NOT NULL,
    session_id      TEXT NOT NULL,
    sequence_num    INTEGER NOT NULL,
    actor           TEXT NOT NULL DEFAULT 'unknown',
    agent_id        TEXT,
    command         TEXT NOT NULL,
    arguments       JSONB,
    exit_code       INTEGER,
    duration_ms     BIGINT,
    working_dir     TEXT,
    repository      TEXT,
    branch          TEXT,
    stdout_hash     TEXT,
    stderr_hash     TEXT,
    has_error       BOOLEAN NOT NULL DEFAULT FALSE,
    tags            JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_exec_events_timestamp ON execution_events (timestamp);
CREATE INDEX IF NOT EXISTS idx_exec_events_source ON execution_events (source);
CREATE INDEX IF NOT EXISTS idx_exec_events_session ON execution_events (session_id);
CREATE INDEX IF NOT EXISTS idx_exec_events_command ON execution_events (command);
CREATE INDEX IF NOT EXISTS idx_exec_events_has_error ON execution_events (has_error) WHERE has_error = TRUE;
CREATE INDEX IF NOT EXISTS idx_exec_events_actor ON execution_events (actor);

CREATE TABLE IF NOT EXISTS ingestion_checkpoints (
    adapter         TEXT PRIMARY KEY,
    last_run_id     TEXT,
    last_run_at     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMIT;
