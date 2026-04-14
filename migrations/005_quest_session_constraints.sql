-- 005_quest_session_constraints.sql
-- Harden quest_session with CHECK constraints on status + rarity enums.
-- Findings: wiki/ganglia/campaign-quest/phase1-first-law-sheet.md (lovelace)
-- Campaign: docs/strategy/quest-campaign-charter-2026-04-14.md
--
-- status enum reflects:
--   'pending' (McGonigal veto gate, new)
--   'live'    (in progress)
--   'won'     (completed / objective down)
--   'grey'    (rested / no-op, new — replaces xp=0 convention)
--   'abandoned' (close-trigger retune, new)
--
-- rarity enum replaces the old 3-label set (first_blood/flawless/overnight)
-- with a 6-tier loot scale: grey → common → uncommon → rare → epic → legendary.
--
-- Note: if production rows already carry old rarity labels
-- (first_blood/flawless/overnight), this migration will fail until those are
-- renamed. The campaign charter mandates the new scale, so we surface that
-- failure loudly rather than silently accepting drift.

ALTER TABLE quest_session
  ADD CONSTRAINT quest_session_status_chk
  CHECK (status IN ('pending', 'live', 'won', 'grey', 'abandoned'));

ALTER TABLE quest_session
  ADD CONSTRAINT quest_session_rarity_chk
  CHECK (rarity IS NULL OR rarity IN ('grey', 'common', 'uncommon', 'rare', 'epic', 'legendary'));

-- Lifecycle invariant: a session has no end time iff it is still active
-- (pending or live). Once won/grey/abandoned, ended_at must be set.
ALTER TABLE quest_session
  ADD CONSTRAINT quest_session_lifecycle_chk
  CHECK (
    (ended_at IS NULL     AND status IN ('pending', 'live'))
    OR
    (ended_at IS NOT NULL AND status IN ('won', 'grey', 'abandoned'))
  );
