-- scripts/governance_analysis.sql
-- Curie governance analysis: before deleting "orphan" emitters (sprint_*,
-- standup_*, org_chart, leaderboard, coord_*, soulforge_scorecard, hunt_xp),
-- answer: do ANY emitters have readers? Is the zero-reader property general
-- or specific? Is chitin kernel emitting? What lives in governance_events?
--
-- READ-ONLY. Follow-up to scripts/orphan_emitters.sql (shannon audit).
-- Usage:  psql "$NEON_DATABASE_URL" -f scripts/governance_analysis.sql

\timing off
\pset pager off

\echo ==========================================================
\echo curie governance analysis — pre-delete evidence sweep
\echo ==========================================================

---------------------------------------------------------------
-- 1. FULL TOOL-CALL HISTOGRAM
---------------------------------------------------------------
\echo
\echo --- 1a. execution_events: command top 30 ---
SELECT command, count(*) AS n
FROM execution_events
GROUP BY command ORDER BY n DESC LIMIT 30;

\echo --- 1b. execution_events by source ---
SELECT source, count(*) AS n
FROM execution_events GROUP BY source ORDER BY n DESC;

\echo --- 1c. governance_events: action top 30 ---
SELECT action, count(*) AS n
FROM governance_events
GROUP BY action ORDER BY n DESC LIMIT 30;

\echo --- 1d. governance_events by event_source ---
SELECT event_source, count(*) FROM governance_events
GROUP BY event_source ORDER BY count(*) DESC;

\echo --- 1e. governance_events by driver_type ---
SELECT driver_type, count(*) FROM governance_events
GROUP BY driver_type ORDER BY count(*) DESC;

---------------------------------------------------------------
-- 2. READERS: insights + health_scores + acknowledged
---------------------------------------------------------------
\echo
\echo --- 2a. insights total (expect: empty) ---
SELECT count(*) AS total_insights FROM insights;
SELECT category, count(*) FROM insights GROUP BY category;
SELECT acknowledged, count(*) FROM insights GROUP BY acknowledged;

\echo --- 2b. health_scores: only real reader ---
SELECT scope_type, count(*) FROM health_scores GROUP BY scope_type;
SELECT scope_type, scope_value, score, timestamp
FROM health_scores ORDER BY timestamp DESC LIMIT 10;

---------------------------------------------------------------
-- 3. GOVERNANCE BREAKDOWN
---------------------------------------------------------------
\echo
\echo --- 3a. event_type (expect: only tool_call) ---
SELECT event_type, count(*) FROM governance_events
GROUP BY event_type ORDER BY count(*) DESC;

\echo --- 3b. outcome ---
SELECT outcome, count(*) FROM governance_events
GROUP BY outcome ORDER BY count(*) DESC;

\echo --- 3c. risk_level ---
SELECT risk_level, count(*) FROM governance_events
GROUP BY risk_level ORDER BY count(*) DESC;

\echo --- 3d. deny reasons (top 15) ---
SELECT metadata->>'reason' AS reason, count(*) AS n
FROM governance_events WHERE outcome='deny'
GROUP BY reason ORDER BY n DESC LIMIT 15;

\echo --- 3e. denied actions (top 15) ---
SELECT action, count(*) FROM governance_events WHERE outcome='deny'
GROUP BY action ORDER BY count(*) DESC LIMIT 15;

---------------------------------------------------------------
-- 4. CHITIN KERNEL INTEGRATION
---------------------------------------------------------------
\echo
\echo --- 4a. chitin_runtime events (execution_events.source) ---
SELECT command, count(*) FROM execution_events
WHERE source='chitin_runtime' GROUP BY command ORDER BY count(*) DESC;

\echo --- 4b. chitin-hook driver in governance_events ---
SELECT action, outcome, count(*) FROM governance_events
WHERE driver_type='chitin-hook'
GROUP BY action, outcome ORDER BY count(*) DESC LIMIT 20;

\echo --- 4c. flow.chitin.hook.pretool — allow vs deny ---
SELECT outcome, count(*) FROM governance_events
WHERE action='flow.chitin.hook.pretool'
GROUP BY outcome;

---------------------------------------------------------------
-- 5. READERSHIP SYMMETRY
--    For each emitter family: events written, has_reader?
--    Readers known today:
--      - insights.evidence              (EMPTY)
--      - health_scores.dimensions       (platform/queue/repo)
--      - analyzer/unacked.go            (reads insights)
--    If NOTHING reads emitter X, deleting X is isomorphic to
--    deleting every other orphan. Prove this is general.
---------------------------------------------------------------
\echo
\echo --- 5. emitter family vs downstream reader ---
WITH p AS (
  SELECT label, re FROM (VALUES
    ('sprint_',      'sprint_'),
    ('standup_',     'standup_'),
    ('org_chart',    'org_chart'),
    ('leaderboard',  'leaderboard'),
    ('coord_',       'coord_'),
    ('scorecard',    'scorecard'),
    ('hunt',         'hunt'),
    ('soulforge',    'soulforge'),
    ('chitin_hook',  'chitin.hook'),
    ('sentinel',     'sentinel'),
    ('swarm',        'swarm'),
    ('mcp_octi',     'mcp__octi'),
    ('wiki',         'wiki')
  ) AS t(label, re)
)
SELECT
  p.label,
  (SELECT count(*) FROM execution_events e
    WHERE e.command ILIKE '%'||p.re||'%'
       OR e.tags::text ILIKE '%'||p.re||'%'
       OR e.arguments::text ILIKE '%'||p.re||'%')      AS exec_hits,
  (SELECT count(*) FROM governance_events g
    WHERE g.action ILIKE '%'||p.re||'%'
       OR g.event_type ILIKE '%'||p.re||'%'
       OR g.metadata::text ILIKE '%'||p.re||'%')       AS gov_hits,
  (SELECT count(*) FROM insights i
    WHERE i.narrative ILIKE '%'||p.re||'%'
       OR i.evidence::text ILIKE '%'||p.re||'%')       AS insight_hits,
  (SELECT count(*) FROM health_scores h
    WHERE h.scope_value ILIKE '%'||p.re||'%'
       OR h.dimensions::text ILIKE '%'||p.re||'%')     AS health_hits
FROM p
ORDER BY gov_hits + exec_hits DESC;
