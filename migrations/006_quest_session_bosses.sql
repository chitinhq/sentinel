-- 006_quest_session_bosses.sql
-- Phase 3 boss mechanics: add bosses_engaged column to quest_session.
-- Spec: workspace:docs/strategy/quest-phase3-boss-spec-2026-04-14.md §2, §4.1
--
-- Commit-then-reveal protocol:
--   - Bosses engaged at /quest open are written to this column.
--   - At /quest close, claimed boss-kill drops are cross-checked against this
--     set; unmatched kills are demoted to regular drops (script-side validation).
--
-- PROVISIONAL (Phase 3 shipped before entry criteria in spec §8): detection
-- thresholds and rarity guardrail will refine against real data.
--
-- Additive migration — no ALTER of existing columns.

ALTER TABLE quest_session
  ADD COLUMN IF NOT EXISTS bosses_engaged JSONB NOT NULL DEFAULT '[]'::jsonb;

-- Structural check: bosses_engaged must be a JSON array.
-- Per-element shape validation happens at write-time in scripts/quest-log.sh
-- (Postgres CHECK on per-element JSONB structure is awkward; documented here).
ALTER TABLE quest_session
  DROP CONSTRAINT IF EXISTS quest_session_bosses_engaged_chk;

ALTER TABLE quest_session
  ADD CONSTRAINT quest_session_bosses_engaged_chk
  CHECK (jsonb_typeof(bosses_engaged) = 'array');

COMMENT ON COLUMN quest_session.bosses_engaged IS
  'JSONB array of {name, kind} objects. kind in (red_ci_dragon, flaky_test_hydra, stale_branch_leviathan). Written at /quest open; used by /quest close to validate boss_kill drops (commit-then-reveal, no retroactive bosses). Phase 3 provisional.';
