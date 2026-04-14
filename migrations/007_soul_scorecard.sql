-- 007_soul_scorecard.sql
-- Phase 4 scaffolding: multi-session soul progression.
-- Spec: workspace:docs/strategy/quest-campaign-charter-2026-04-14.md (Phase 4)
--
-- Per jokic's governance rule "count assists, not points," each closed session
-- records which souls led vs. shipped-in-support. Aggregated across sessions,
-- this reveals each soul's real-world impact — the scoreboard the game is
-- actually keeping.
--
-- This migration is SCAFFOLDING. No automatic behavior activates; it only
-- gives scripts/soul-scorecard-sync.sh a place to write. Interlock mechanics
-- (Phase 4 proper) read from here once data accumulates.
--
-- Additive. IF NOT EXISTS throughout.

CREATE TABLE IF NOT EXISTS soul_scorecard (
  soul            TEXT NOT NULL,
  metric_name     TEXT NOT NULL,   -- e.g. 'sessions_led', 'sessions_shipped_in',
                                   -- 'sessions_grey', 'vetoes_called', 'slices_dropped'
  metric_value    INTEGER NOT NULL DEFAULT 0,
  last_session_id TEXT,
  last_event_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (soul, metric_name)
);

CREATE INDEX IF NOT EXISTS soul_scorecard_last_event_idx
  ON soul_scorecard(last_event_at DESC);

COMMENT ON TABLE soul_scorecard IS
  'Phase 4 scaffolding. Per-(soul, metric) tallies aggregated from quest_session.party + status. Written by scripts/soul-scorecard-sync.sh. Read by future Phase 4 interlock logic.';
