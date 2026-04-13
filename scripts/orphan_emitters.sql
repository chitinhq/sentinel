-- scripts/orphan_emitters.sql
-- Shannon audit strike: evidence for Quorum v2 hopper delete-list.
-- Question: are the squad-era emitters (sprint_*, standup_*, org_chart,
-- agent_leaderboard, coord_*, soulforge scorecard, hunt XP) actually firing?
-- Do their events have any downstream reader?
--
-- READ-ONLY. Safe to re-run.
-- Usage:  psql "$NEON_DATABASE_URL" -f scripts/orphan_emitters.sql

\timing off
\pset pager off

\echo ==========================================================
\echo orphan emitter scan — Shannon / Quorum v2 week-1 pre-work
\echo ==========================================================

\echo
\echo --- 1. execution_events: overall shape ---
SELECT count(*)                AS total_events,
       min(timestamp)          AS earliest,
       max(timestamp)          AS latest
FROM execution_events;

SELECT source, count(*) AS n, max(timestamp) AS last_seen
FROM execution_events GROUP BY source ORDER BY n DESC;

\echo
\echo --- 2. orphan emitter scan over execution_events ---
\echo (columns: command, actor, tags::text, arguments::text)
WITH patterns(label, re) AS (VALUES
  ('sprint_*',            'sprint_'),
  ('standup_*',           'standup_'),
  ('org_chart',           'org_chart'),
  ('agent_leaderboard',   'leaderboard'),
  ('coord_*',             'coord_'),
  ('soulforge_scorecard', 'scorecard'),
  ('hunt_xp',             'xp')
)
SELECT p.label,
       count(e.*)         AS hits,
       max(e.timestamp)   AS last_seen,
       min(e.timestamp)   AS first_seen
FROM patterns p
LEFT JOIN execution_events e
  ON e.command         ILIKE '%'||p.re||'%'
  OR e.actor           ILIKE '%'||p.re||'%'
  OR e.tags::text      ILIKE '%'||p.re||'%'
  OR e.arguments::text ILIKE '%'||p.re||'%'
GROUP BY p.label ORDER BY hits DESC;

\echo
\echo --- 3. orphan emitter scan over governance_events ---
\echo (columns: event_type, action, resource, metadata::text)
WITH patterns(label, re) AS (VALUES
  ('sprint_*',            'sprint_'),
  ('standup_*',           'standup_'),
  ('org_chart',           'org_chart'),
  ('agent_leaderboard',   'leaderboard'),
  ('coord_*',             'coord_'),
  ('soulforge_scorecard', 'scorecard'),
  ('hunt_xp',             'xp')
)
SELECT p.label,
       count(e.*)         AS hits,
       max(e.timestamp)   AS last_seen,
       min(e.timestamp)   AS first_seen
FROM patterns p
LEFT JOIN governance_events e
  ON e.event_type    ILIKE '%'||p.re||'%'
  OR e.action        ILIKE '%'||p.re||'%'
  OR e.resource      ILIKE '%'||p.re||'%'
  OR e.metadata::text ILIKE '%'||p.re||'%'
GROUP BY p.label ORDER BY hits DESC;

\echo
\echo --- 4. quest_session — does hunt XP actually persist? ---
SELECT count(*)                                    AS total_sessions,
       count(*) FILTER (WHERE xp_awarded > 0)      AS sessions_with_xp,
       max(started_at)                             AS last_started,
       max(ended_at)                               AS last_ended
FROM quest_session;

\echo
\echo --- 5. reader check: insights referencing orphan terms ---
SELECT category, count(*) AS n
FROM insights
WHERE narrative       ILIKE ANY(ARRAY['%sprint%','%standup%','%org_chart%','%leaderboard%','%coord_%','%scorecard%','%hunt xp%'])
   OR evidence::text  ILIKE ANY(ARRAY['%sprint%','%standup%','%org_chart%','%leaderboard%','%coord_%','%scorecard%','%hunt xp%'])
GROUP BY category;

\echo
\echo --- 6. reader check: health_scores scope types ---
SELECT scope_type, count(*) FROM health_scores GROUP BY scope_type;

\echo
\echo --- 7. reader check: foreign keys referencing emitter tables ---
SELECT conname,
       conrelid::regclass  AS from_table,
       confrelid::regclass AS to_table
FROM pg_constraint
WHERE contype = 'f'
  AND confrelid::regclass::text IN ('execution_events','quest_session','governance_events');

\echo
\echo --- 8. sample of orphan governance rows (last 25) ---
SELECT action, agent_name, timestamp
FROM governance_events
WHERE action   ILIKE ANY(ARRAY['%sprint%','%standup%','%org_chart%','%leaderboard%','%coord_%','%scorecard%'])
   OR resource ILIKE ANY(ARRAY['%sprint%','%standup%','%org_chart%','%leaderboard%','%coord_%','%scorecard%'])
ORDER BY timestamp DESC
LIMIT 25;
