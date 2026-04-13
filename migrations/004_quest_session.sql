-- 004_quest_session.sql
-- Quest MVP — one row per /go session, rendered to public HTML by chitinhq/quest.
-- Spec: workspace:docs/superpowers/specs/2026-04-13-hunt-mvp-design.md
-- Boundary: workspace:docs/strategy/soulforge-hunt-boundary-2026-04-13.md
--   party[].soul lives here (DB is private). The renderer strips it before
--   committing to gh-pages; only party[].class reaches public HTML.
-- Renamed from hunt_session under workspace#382.

CREATE TABLE IF NOT EXISTS quest_session (
  session_id    TEXT PRIMARY KEY,
  started_at    TIMESTAMPTZ NOT NULL,
  ended_at      TIMESTAMPTZ,
  theme         TEXT NOT NULL DEFAULT 'ff',
  party_name    TEXT NOT NULL,
  encounter     TEXT NOT NULL,
  party         JSONB NOT NULL,
  objective     JSONB NOT NULL DEFAULT '[]',
  loot          JSONB NOT NULL DEFAULT '[]',
  moves         JSONB NOT NULL DEFAULT '[]',
  xp_awarded    INT NOT NULL DEFAULT 0,
  headline      TEXT,
  narrative     TEXT,
  rarity        TEXT,
  repo          TEXT,
  status        TEXT NOT NULL DEFAULT 'live',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS quest_session_ended_idx ON quest_session (ended_at DESC);
CREATE INDEX IF NOT EXISTS quest_session_party_idx ON quest_session (party_name);
