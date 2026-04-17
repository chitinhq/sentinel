# Spec: Sentinel Analytics Layer (Python)

**Author:** Curie soul · **Date:** 2026-04-13 · **Status:** Draft

## Problem

Sentinel ingests governance + flow events into Neon Postgres. The Go `sentinel mine` subcommand copies events into an `analytics.*` schema, but no pattern mining happens — the data sits there. We can't currently answer:

- Which tool-call *sequences* precede failure?
- Which (soul × driver × tool) combinations are fragile?
- What free-text failure reasons cluster into candidate invariants?
- Which hooks actually *prevented* harm vs fired without effect?
- Is the trace-shape of a failing run anomalous before the failure?

Go is the wrong tool for this. Pandas/polars/duckdb/scikit/networkx exist; rewriting them in Go is not a good use of time.

## Proposal

Add `sentinel/analytics/` — a Python package that reads the existing Neon tables and produces artifacts Sentinel (Go) can consume:

1. **Invariant proposals** (JSON → `analytics.invariant_proposals` table, reviewed by humans, promoted to `chitin.yaml`)
2. **Scorecards** (per-soul, per-driver, per-repo — served via `sentinel flows`)
3. **Notebooks** (checked in, run weekly, outputs to `wiki/ganglia/sentinel/`)

**No change** to the Go ingester or runtime. Additive only.

## Scope (Phase 1 — 1 week)

Five passes, each a standalone script + notebook. All read `analytics.execution_events`.

### Pass 1 — Sequence mining
- Library: `prefixspan` (Python) or `duckdb` window functions
- Input: event stream grouped by `session_id`, ordered by `ts`, projected to `(tool, outcome)` pairs
- Output: top 20 frequent subsequences preceding `outcome=failure`
- Success: surface ≥3 subsequences with support > 5% that we don't already have invariants for

### Pass 2 — Fragility heatmap
- Group by (soul, driver, tool), compute failure_rate + event count
- Output: heatmap PNG + JSON of cells with rate > 20% AND n > 50
- Success: identify ≥1 (soul, driver) combo we should stop pairing

### Pass 3 — Failure-reason clustering
- Embed free-text `failure_reason` strings with a small local model (bge-small, runs on the M4)
- HDBSCAN cluster, top-k exemplars per cluster
- Output: proto-invariant list — one per cluster with representative text
- Success: ≥5 clusters → ≥5 candidate invariants filed as GH issues

### Pass 4 — Hook effectiveness
- For each hook, count (fired, ack=allow, ack=deny, post-deny failure)
- Compute lift: does firing the hook reduce downstream failure vs baseline?
- Output: per-hook table, flag hooks with lift ≈ 0 as dead weight
- Success: identify ≥2 hooks to retire or tighten

### Pass 5 — Trace-shape anomaly detection
- Feature: tool-call count distribution per session (bag of tools)
- Model: isolation forest
- Output: top anomalous sessions for manual review — feed back into Pass 3 embedding
- Success: weekly top-10 list; ≥2 real issues found across 4 weeks

## Deliverables

```
sentinel/
├── analytics/
│   ├── pyproject.toml          # uv-managed, duckdb + polars + prefixspan + hdbscan + sklearn
│   ├── README.md
│   ├── lib/
│   │   ├── db.py               # Neon connection (re-uses sentinel's DATABASE_URL)
│   │   ├── events.py           # event-stream loaders (duckdb attach)
│   │   └── artifacts.py        # writes JSON → analytics.invariant_proposals
│   ├── passes/
│   │   ├── 01_sequence_mining.py
│   │   ├── 02_fragility_heatmap.py
│   │   ├── 03_failure_clusters.py
│   │   ├── 04_hook_effectiveness.py
│   │   └── 05_anomaly_detection.py
│   ├── notebooks/
│   │   └── *.ipynb             # one per pass, interactive version
│   └── scripts/
│       └── weekly_run.sh       # runs all 5 passes, commits outputs to wiki
└── migrations/
    └── 20260413_analytics_invariant_proposals.sql
```

New table:

```sql
CREATE TABLE analytics.invariant_proposals (
  id           bigserial PRIMARY KEY,
  proposed_at  timestamptz NOT NULL DEFAULT now(),
  pass         text NOT NULL,           -- which analysis produced it
  evidence_n   integer NOT NULL,        -- sample size
  support      real NOT NULL,           -- fraction of sessions
  summary      text NOT NULL,
  payload      jsonb NOT NULL,          -- pass-specific data
  status       text NOT NULL DEFAULT 'proposed'  -- proposed|reviewed|accepted|rejected
);
```

## Non-goals

- No real-time analytics (passes run weekly, not per-event)
- No replacement of Go ingester or runtime
- No ML training loop — static models only (isolation forest, HDBSCAN, embeddings)
- No dashboard UI — outputs are JSON/PNG + wiki markdown

## Risks

- **Neon credentials in Python** — mitigate by reusing sentinel's `DATABASE_URL` from `.env`, never committing
- **Small-N on some slices** — enforce `min_support` thresholds per pass; don't propose invariants under 50 events
- **Embedding cost** — use a local sentence-transformer (bge-small), not an API

## Rollout

- Phase 1 (this spec, 1 week): land `analytics/` dir + passes 1, 2, 4 (the three that need no local model)
- Phase 2 (+1 week): passes 3, 5 (need embeddings)
- Phase 3 (+1 week): weekly cron + automatic issue filing for high-support proposals

## Success metric

At 4 weeks: ≥10 invariant proposals filed as GH issues, ≥3 promoted to `chitin.yaml`. If <3 promotions, kill the pass with the worst hit rate.

## Open questions

- Should this live in `sentinel/analytics/` or a separate `sentinel-analytics` repo? Leaning same-repo — analytics reads the same schema, shares auth, tightens the loop. Split only if the Python deps bloat the Go image.
- Run location: local (M4) or a remote runner? Start local, move to remote if passes exceed 10 minutes.
