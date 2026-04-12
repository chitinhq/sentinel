package ingestion

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeJSONL appends the given lines to a file as JSON-encoded
// events. Creates parent dir if needed.
func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, ln := range lines {
		if err := json.NewEncoder(f).Encode(ln); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeAdapter_IngestSessionLifecycle(t *testing.T) {
	stateDir := t.TempDir()
	shareDir := t.TempDir()

	writeJSONL(t, filepath.Join(stateDir, "session-events.log"), []map[string]any{
		{
			"event": "session_started", "ts": "2026-04-12T12:00:00Z",
			"session_id": "sess_A", "driver": "claude-code", "soul": "feynman",
			"role": "design", "model": "opus",
		},
		{
			"event": "session_rated", "ts": "2026-04-12T12:05:00Z",
			"session_id": "sess_A", "rating": "good", "note": "clean",
		},
		{
			"event": "session_ended", "ts": "2026-04-12T12:10:00Z",
			"session_id": "sess_A", "duration_ms": float64(600000),
			"reason": "wrapper_exit",
		},
	})

	adapter := NewChitinRuntimeAdapter(stateDir, shareDir)
	events, cp, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}

	// Session started preserves fingerprint.
	start := events[0]
	if start.Command != "session_started" || start.Tags["soul"] != "feynman" {
		t.Errorf("session_started mapped wrong: %+v", start)
	}
	if start.AgentID != "claude-code" {
		t.Errorf("driver not propagated to AgentID: %+v", start)
	}

	// Rating maps to exit code (good = 0).
	rated := events[1]
	if rated.ExitCode == nil || *rated.ExitCode != 0 {
		t.Errorf("good rating should be exit 0, got %+v", rated.ExitCode)
	}
	if rated.HasError {
		t.Error("good rating should not be an error")
	}
	if rated.Tags["rating"] != "good" || rated.Tags["note"] != "clean" {
		t.Errorf("rating tags lost: %+v", rated.Tags)
	}

	// Ended carries duration_ms.
	ended := events[2]
	if ended.DurationMs == nil || *ended.DurationMs != 600000 {
		t.Errorf("duration not propagated: %+v", ended.DurationMs)
	}

	// Checkpoint is non-empty so next run resumes from the right place.
	if cp == nil || cp.LastRunID == "" {
		t.Error("checkpoint not advanced")
	}
}

func TestRuntimeAdapter_GateResult(t *testing.T) {
	stateDir := t.TempDir()
	shareDir := t.TempDir()

	writeJSONL(t, filepath.Join(shareDir, "gate-events.log"), []map[string]any{
		{
			"gate": "planning", "name": "planning/check_acceptance_criteria",
			"result": "fail", "reason": "missing AC section",
			"repo": "octi", "issue": "119", "queue": "intake",
			"session_id": "sess_B", "ts": "2026-04-12T12:00:00Z",
		},
		{
			"gate": "validate", "name": "validate/check_ci_passed",
			"result": "pass", "reason": "CI passed on PR #42",
			"repo": "octi", "issue": "119", "queue": "validate",
			"session_id": "sess_B", "ts": "2026-04-12T12:10:00Z",
		},
	})

	events, _, err := NewChitinRuntimeAdapter(stateDir, shareDir).Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 gate events, got %d", len(events))
	}

	fail := events[0]
	if fail.Command != "gate.result" || *fail.ExitCode != 1 {
		t.Errorf("gate fail mapped wrong: %+v", fail)
	}
	if fail.Tags["gate"] != "planning" || fail.Tags["result"] != "fail" {
		t.Errorf("gate tags lost: %+v", fail.Tags)
	}
	if fail.Repository != "octi" {
		t.Errorf("repo not propagated: %+v", fail)
	}

	pass := events[1]
	if *pass.ExitCode != 0 || pass.HasError {
		t.Errorf("pass should be exit 0 and not an error: %+v", pass)
	}
}

func TestRuntimeAdapter_SoulToggle(t *testing.T) {
	stateDir := t.TempDir()

	writeJSONL(t, filepath.Join(stateDir, "soul-events.log"), []map[string]any{
		{
			"event": "soul_activated", "ts": "2026-04-12T12:00:00Z",
			"soul": "feynman", "by": "jared",
			"targets": []any{"/home/x/.claude/CLAUDE.md"},
		},
		{
			"event": "soul_deactivated", "ts": "2026-04-12T13:00:00Z",
			"soul": "feynman", "by": "jared",
		},
	})

	events, _, err := NewChitinRuntimeAdapter(stateDir, "").Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 soul events, got %d", len(events))
	}

	on := events[0]
	if on.Command != "soul_activated" || on.Tags["soul"] != "feynman" {
		t.Errorf("soul_activated mapped wrong: %+v", on)
	}
	// Soul toggles come from users, not agents.
	if on.Actor != ActorHuman {
		t.Errorf("soul events should be human actor, got %s", on.Actor)
	}
	if on.SessionID != "soul:feynman" {
		t.Errorf("soul session id pseudo-key wrong: %s", on.SessionID)
	}
}

func TestRuntimeAdapter_CheckpointResume(t *testing.T) {
	stateDir := t.TempDir()
	shareDir := t.TempDir()
	logPath := filepath.Join(stateDir, "session-events.log")

	writeJSONL(t, logPath, []map[string]any{
		{"event": "session_started", "ts": "2026-04-12T12:00:00Z", "session_id": "A"},
	})

	adapter := NewChitinRuntimeAdapter(stateDir, shareDir)
	events1, cp1, _ := adapter.Ingest(context.Background(), nil)
	if len(events1) != 1 {
		t.Fatalf("first ingest got %d events, want 1", len(events1))
	}

	// Append a second event and re-ingest with the prior checkpoint.
	writeJSONL(t, logPath, []map[string]any{
		{"event": "session_ended", "ts": "2026-04-12T12:05:00Z", "session_id": "A"},
	})

	events2, _, _ := adapter.Ingest(context.Background(), cp1)
	if len(events2) != 1 {
		t.Errorf("resumed ingest should return only the new event, got %d", len(events2))
	}
	if events2[0].Command != "session_ended" {
		t.Errorf("resumed event should be session_ended, got %s", events2[0].Command)
	}
}

func TestRuntimeAdapter_MalformedLinesSkipped(t *testing.T) {
	stateDir := t.TempDir()
	logPath := filepath.Join(stateDir, "session-events.log")

	// Mix valid and invalid JSON lines.
	f, _ := os.Create(logPath)
	f.WriteString(`{"event":"session_started","ts":"2026-04-12T12:00:00Z","session_id":"X"}` + "\n")
	f.WriteString("this is not json\n")
	f.WriteString(`{"event":"session_ended","ts":"2026-04-12T12:05:00Z","session_id":"X"}` + "\n")
	f.WriteString("\n") // blank line
	f.Close()

	events, _, _ := NewChitinRuntimeAdapter(stateDir, "").Ingest(context.Background(), nil)
	if len(events) != 2 {
		t.Errorf("malformed lines not skipped: got %d events, want 2", len(events))
	}
}

func TestParseExitFromReason(t *testing.T) {
	cases := map[string]struct {
		code int
		ok   bool
	}{
		"wrapper_exit_rc0": {0, true},
		"wrapper_exit_rc1": {1, true},
		"wrapper_exit_rc255": {255, true},
		"wrapper_exit":     {0, false},
		"user_ended":       {0, false},
		"":                 {0, false},
	}
	for in, want := range cases {
		got, ok := parseExitFromReason(in)
		if got != want.code || ok != want.ok {
			t.Errorf("parseExitFromReason(%q) = (%d, %v), want (%d, %v)",
				in, got, ok, want.code, want.ok)
		}
	}
}

func TestRatingExitCode(t *testing.T) {
	cases := map[string]int{
		"good":    0,
		"bad":     1,
		"mixed":   2,
		"unknown": 2, // fail-closed default
	}
	for rating, want := range cases {
		if got := ratingExitCode(rating); got != want {
			t.Errorf("ratingExitCode(%q) = %d, want %d", rating, got, want)
		}
	}
}
