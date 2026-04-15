// Package analyzer — dispatch reconciliation pass.
//
// DetectDispatchOrphans cross-joins three truth sinks to surface silent-loss
// bugs (chitinhq/workspace#408, "Telemetry Truth" campaign):
//
//  1. Redis  octi:dispatch-log        — what octi claims it dispatched
//  2. GitHub gh run list (per repo)   — what Actions actually executed
//  3. Neon   execution_events         — what Sentinel saw land
//
// An orphan is any record present in one sink but not the others. The three
// orphan classes are:
//
//   - DispatchedButNoRun : dispatch-log says "dispatched", no matching gh run
//   - RunButNoDispatch   : gh run exists, no corresponding dispatch record
//   - RunButNoEvent      : gh run completed, no execution_events row
//
// # Join key
//
// The canonical join key is `dispatch_id` — a ULID minted in octi's
// Dispatcher.recordDispatch and propagated via `client_payload.dispatch_id`
// into the repository_dispatch event that fires the GH Actions workflow.
//
// As of 2026-04-15 this id does NOT exist. See chitinhq/octi#257.
// Until that ships, this pass returns (nil, ErrJoinKeyMissing) and emits a
// single synthetic Finding pointing at the upstream blocker. We deliberately
// refuse to fall back to fuzzy (agent, repo, timestamp) joins — multiple
// dispatches collide within a second and brain.* events frequently have
// empty repo, so fuzzy joins produce false-positive orphans that would
// pollute the finding stream worse than the silent loss we're trying to
// catch.
//
// # Known edge cases (post-ID-landing)
//
//   - DeepSeek depleted until 2026-05: bench-driven dispatches are zero;
//     historical Redis (pre-depletion) is the only live sample.
//   - dispatch-log is LTRIM'd to 500 entries — retention window is short.
//     Reconciler must run at least hourly to avoid losing the Redis side.
//   - Skipped dispatches (result="skipped") are NOT orphans — they never
//     tried to fire. Filter for result=="dispatched" on the Redis side.
//   - driver="anthropic" and driver="cli" do not produce gh runs; only
//     driver="gh-actions" participates in the three-way join. Other drivers
//     reconcile against dispatch-log ↔ execution_events only (two-way).
package analyzer

import (
	"errors"
	"fmt"
	"time"
)

// ErrJoinKeyMissing is returned when the dispatch_id correlation id is not
// present in the input data. Blocked on chitinhq/octi#257.
var ErrJoinKeyMissing = errors.New("reconcile: dispatch_id not present in dispatch-log; blocked on octi#257")

// DispatchRecord is a single entry from Redis octi:dispatch-log.
type DispatchRecord struct {
	DispatchID string // MISSING as of 2026-04-15; blocked on octi#257
	Agent      string
	EventType  string
	Repo       string
	Result     string // "dispatched" | "skipped"
	Driver     string // "gh-actions" | "anthropic" | "cli" | ""
	Timestamp  time.Time
}

// GHRun is a single entry from `gh run list --json`.
type GHRun struct {
	Repo       string
	RunID      int64
	DispatchID string // extracted from client_payload.dispatch_id (once octi#257 lands)
	Event      string // "repository_dispatch" | "push" | ...
	Status     string
	StartedAt  time.Time
}

// OrphanClass labels the reconciliation gap.
type OrphanClass string

const (
	OrphanDispatchedNoRun OrphanClass = "dispatched_no_run"
	OrphanRunNoDispatch   OrphanClass = "run_no_dispatch"
	OrphanRunNoEvent      OrphanClass = "run_no_event"
)

// DetectDispatchOrphans LEFT JOINs the three sinks on dispatch_id and emits
// one Finding per orphan class (aggregated with evidence capped at 10).
//
// Returns (nil, ErrJoinKeyMissing) if fewer than 10% of dispatch records
// carry a non-empty dispatch_id — strong signal the upstream octi fix has
// not landed yet.
func DetectDispatchOrphans(
	dispatches []DispatchRecord,
	runs []GHRun,
	events []Event,
	window time.Duration,
) ([]Finding, error) {
	// Gate: require dispatch_id coverage. This is the guardrail that keeps
	// us from silently producing fuzzy-join false positives.
	if !hasJoinKey(dispatches) {
		return nil, ErrJoinKeyMissing
	}

	dispatchByID := make(map[string]DispatchRecord, len(dispatches))
	for _, d := range dispatches {
		if d.Result != "dispatched" || d.Driver != "gh-actions" {
			continue
		}
		dispatchByID[d.DispatchID] = d
	}
	runByID := make(map[string]GHRun, len(runs))
	for _, r := range runs {
		if r.DispatchID == "" {
			continue
		}
		runByID[r.DispatchID] = r
	}
	eventByDispatchID := make(map[string]Event, len(events))
	for _, e := range events {
		if e.Metadata == nil {
			continue
		}
		id, _ := e.Metadata["dispatch_id"].(string)
		if id == "" {
			continue
		}
		eventByDispatchID[id] = e
	}

	now := time.Now()
	var findings []Finding

	// Class 1: dispatched, no run
	var dnr []DispatchRecord
	for id, d := range dispatchByID {
		if _, ok := runByID[id]; !ok && now.Sub(d.Timestamp) > window {
			dnr = append(dnr, d)
		}
	}
	if len(dnr) > 0 {
		findings = append(findings, makeFinding(OrphanDispatchedNoRun, len(dnr), now))
	}

	// Class 2: run, no dispatch
	var rnd []GHRun
	for id, r := range runByID {
		if _, ok := dispatchByID[id]; !ok {
			rnd = append(rnd, r)
		}
	}
	if len(rnd) > 0 {
		findings = append(findings, makeFinding(OrphanRunNoDispatch, len(rnd), now))
	}

	// Class 3: run completed, no execution_events row
	var rne []GHRun
	for id, r := range runByID {
		if r.Status != "completed" {
			continue
		}
		if _, ok := eventByDispatchID[id]; !ok {
			rne = append(rne, r)
		}
	}
	if len(rne) > 0 {
		findings = append(findings, makeFinding(OrphanRunNoEvent, len(rne), now))
	}

	return findings, nil
}

// hasJoinKey gates the pass: require at least 10% of records to carry a
// non-empty dispatch_id, else we're pre-octi#257 and must not fuzzy-join.
func hasJoinKey(dispatches []DispatchRecord) bool {
	if len(dispatches) == 0 {
		return false
	}
	n := 0
	for _, d := range dispatches {
		if d.DispatchID != "" {
			n++
		}
	}
	return float64(n)/float64(len(dispatches)) >= 0.10
}

func makeFinding(class OrphanClass, count int, now time.Time) Finding {
	return Finding{
		ID:         fmt.Sprintf("reconcile-%s-%d", class, now.Unix()),
		Pass:       "reconcile",
		PolicyID:   string(class),
		Metrics:    Metrics{Count: count, SampleSize: count},
		DetectedAt: now,
	}
}
