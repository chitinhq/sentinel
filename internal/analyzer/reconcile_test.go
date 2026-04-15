package analyzer

import (
	"errors"
	"testing"
	"time"
)

func TestDetectDispatchOrphans_BlockedOnMissingJoinKey(t *testing.T) {
	// Real shape circa 2026-04-15: no dispatch_id on any record.
	dispatches := []DispatchRecord{
		{Agent: "workspace-pr-review-agent", Result: "dispatched", Driver: "gh-actions", Timestamp: time.Now()},
		{Agent: "brain-leverage", Result: "skipped", Driver: "", Timestamp: time.Now()},
	}
	_, err := DetectDispatchOrphans(dispatches, nil, nil, time.Minute)
	if !errors.Is(err, ErrJoinKeyMissing) {
		t.Fatalf("expected ErrJoinKeyMissing, got %v", err)
	}
}

func TestDetectDispatchOrphans_ThreeClasses(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * time.Minute)

	dispatches := []DispatchRecord{
		{DispatchID: "d1", Result: "dispatched", Driver: "gh-actions", Timestamp: old}, // → orphan class 1
		{DispatchID: "d2", Result: "dispatched", Driver: "gh-actions", Timestamp: old}, // → matched run+event
		{DispatchID: "d3", Result: "dispatched", Driver: "gh-actions", Timestamp: old}, // → matched run, no event
	}
	runs := []GHRun{
		{DispatchID: "d2", Status: "completed", StartedAt: old},
		{DispatchID: "d3", Status: "completed", StartedAt: old},
		{DispatchID: "dX", Status: "completed", StartedAt: old}, // → orphan class 2
	}
	events := []Event{
		{Metadata: map[string]any{"dispatch_id": "d2"}},
	}

	findings, err := DetectDispatchOrphans(dispatches, runs, events, time.Minute)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings (one per orphan class), got %d", len(findings))
	}
	seen := map[string]int{}
	for _, f := range findings {
		seen[f.PolicyID] = f.Metrics.Count
	}
	if seen[string(OrphanDispatchedNoRun)] != 1 {
		t.Errorf("dispatched_no_run: want 1, got %d", seen[string(OrphanDispatchedNoRun)])
	}
	if seen[string(OrphanRunNoDispatch)] != 1 {
		t.Errorf("run_no_dispatch: want 1, got %d", seen[string(OrphanRunNoDispatch)])
	}
	// d3 (matched dispatch, no event) + dX (no dispatch, no event) both count
	// as run_no_event — the classes intentionally overlap so operators see
	// the full picture of the completed-run side of the join.
	if seen[string(OrphanRunNoEvent)] != 2 {
		t.Errorf("run_no_event: want 2, got %d", seen[string(OrphanRunNoEvent)])
	}
}

func TestDetectDispatchOrphans_SkippedNotOrphan(t *testing.T) {
	now := time.Now()
	dispatches := []DispatchRecord{
		{DispatchID: "d1", Result: "skipped", Driver: "gh-actions", Timestamp: now.Add(-time.Hour)},
	}
	findings, err := DetectDispatchOrphans(dispatches, nil, nil, time.Minute)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("skipped dispatches must not produce orphan findings, got %d", len(findings))
	}
}
