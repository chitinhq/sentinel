-- 008_campaign_achievements.sql
-- Phase 4 scaffolding: cross-session unlocks.
-- Spec: workspace:docs/strategy/quest-campaign-charter-2026-04-14.md (Phase 4)
--
-- A campaign achievement is a stable slug earned when a set of criteria
-- (evaluated against quest_session history) fires. Criteria live in
-- workspace:data/achievements.json and are checked by
-- scripts/campaign-achievements-check.sh.
--
-- Achievements are append-only: once earned, the row is not modified. Later
-- phases MAY add derived tables (e.g. per-session earnings) on top.
--
-- Additive. IF NOT EXISTS throughout.

CREATE TABLE IF NOT EXISTS campaign_achievements (
  achievement_id     TEXT PRIMARY KEY,          -- stable slug: 'first-legendary', 'dragon-triple'
  earned_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  earned_by_session  TEXT NOT NULL,             -- session_id that triggered it
  criteria           JSONB NOT NULL,            -- snapshot of the rule that fired (audit trail)
  display_name       TEXT NOT NULL,
  display_glyph      TEXT NOT NULL DEFAULT '◉'
);

CREATE INDEX IF NOT EXISTS campaign_achievements_earned_at_idx
  ON campaign_achievements(earned_at DESC);

CREATE INDEX IF NOT EXISTS campaign_achievements_session_idx
  ON campaign_achievements(earned_by_session);

COMMENT ON TABLE campaign_achievements IS
  'Phase 4 scaffolding. Append-only cross-session unlocks. Criteria registry in workspace:data/achievements.json. Populated by scripts/campaign-achievements-check.sh.';
