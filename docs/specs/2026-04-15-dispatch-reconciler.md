# Dispatch Reconciler — Three-Sink Truth Join

**Campaign**: chitinhq/workspace#408 Telemetry Truth, Phase-1
**Status**: scaffold shipped, blocked on chitinhq/octi#257
**Owner**: curie
**Date**: 2026-04-15

## Problem

Three "bugs" in #408 share one root cause: three unreconciled truth sinks.
No code LEFT JOINs them, so orphan records hide on each side:

| Sink | Source | Authority over |
|------|--------|----------------|
| Redis `octi:dispatch-log` | `Dispatcher.recordDispatch` (octi) | "Did octi attempt dispatch?" |
| `gh run list` per repo | GitHub Actions | "Did a workflow actually run?" |
| Neon `execution_events` | Sentinel ingestion | "Did Sentinel see it land?" |

**T2 silent-loss**, **benchmark_status lies**, **agent_leaderboard empty** are
all downstream symptoms of this missing reconciliation.

## Approach

One Sentinel detection pass — `DetectDispatchOrphans` — emits one Finding
per orphan class:

- `dispatched_no_run`  — Redis said dispatched, no GH run exists
- `run_no_dispatch`    — GH run exists, no Redis dispatch record
- `run_no_event`       — GH run completed, no Neon execution_events row

Findings route through the standard Sentinel pipeline (analyzer → interpreter
→ router → GitHub issue on kernel repo).

## Join key — THE blocker

Canonical key: **`dispatch_id`** (ULID) minted at dispatch, propagated via
`repository_dispatch.client_payload.dispatch_id` into the workflow run, and
emitted on every execution_event produced by that run.

As of 2026-04-15, this id **does not exist**. Verified by peeking live
Redis on this box: 500 entries in `octi:dispatch-log`, 0 with any
correlation id (task_id / run_id / dispatch_id).

**Upstream fix tracked**: chitinhq/octi#257 — two-file change to
`internal/dispatch/dispatcher.go` plus whichever adapter emits
`repository_dispatch`.

## Why we refuse fuzzy-join

A tempting fallback is `(agent, repo, timestamp ± window)`. We do not
implement this — the brief explicitly forbids synthetic ids, and inspection
of real data shows why:

1. **Timestamp collisions**: `brain.leverage` fires every ~60s and can
   fan out to multiple agents at the same second.
2. **Empty repo field**: `brain.*` events have `repo: ""` frequently —
   nothing to disambiguate on.
3. **False positives poison the signal**: orphan findings that are wrong
   worse than silent loss we're catching; they'd train the team to ignore
   the pass.

The reconciler's `hasJoinKey()` guardrail returns `ErrJoinKeyMissing` when
<10% of records carry a non-empty dispatch_id, so the pass becomes a no-op
until octi#257 ships.

## Known edge cases (post-ID-landing)

- **DeepSeek depleted until 2026-05-01**: bench-driven dispatches are zero;
  historical Redis (pre-depletion) is the only pre-test sample. Reconciler
  should be re-validated once DeepSeek returns.
- **Redis retention is 500 entries (LTRIM)**: at current dispatch rates
  (~1/min), that's ~8 hours. Reconciler must run at least hourly to avoid
  losing the Redis side of the join.
- **`result=skipped`**: not an orphan — dispatch was never attempted.
  Filter to `result=dispatched` before joining.
- **Non-gh-actions drivers** (`anthropic`, `cli`): no GH run produced;
  reconcile two-way (dispatch-log ↔ execution_events) not three-way.

## Verification plan (once unblocked)

1. Ensure octi#257 merged and dispatch_id is appearing in Redis entries.
2. Run `sentinel analyze` against a 24h window.
3. Expect `dispatched_no_run` count ≈ 0 in steady state; any non-zero is
   real silent loss (the #408 target).
4. Expect `run_no_event` count > 0 initially (Sentinel ingestion lag);
   should converge to 0 within TTL window.

## Files

- `internal/analyzer/reconcile.go`     — pass implementation + guardrail
- `internal/analyzer/reconcile_test.go` — unit tests (blocked-state + 3-class)
- This spec

## Verification run (2026-04-15)

Against the live Redis dispatch-log (500 entries):
`DetectDispatchOrphans` returned `ErrJoinKeyMissing` — **as expected**.
Zero orphans reportable until octi#257 lands. The guardrail works.
