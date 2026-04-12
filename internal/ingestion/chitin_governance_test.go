package ingestion

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestChitinGovernanceAdapter_Ingest(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")
	data := `{"ts":"2026-04-11T19:00:00Z","sid":"sess-1","agent":"claude-code","tool":"Bash","action":"exec","path":"","command":"git push origin main","outcome":"deny","reason":"Direct push to protected branch","source":"policy","latency_us":1200}
{"ts":"2026-04-11T19:01:00Z","sid":"sess-1","agent":"claude-code","tool":"Read","action":"read","path":"src/main.go","command":"","outcome":"allow","reason":"","source":"policy","latency_us":500}
`
	os.WriteFile(eventsFile, []byte(data), 0644)

	adapter := NewChitinGovernanceAdapter([]string{dir})
	events, cp, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// First event: deny.
	ev0 := events[0]
	if ev0.Source != SourceChitinGovernance {
		t.Errorf("expected source chitin_governance, got %s", ev0.Source)
	}
	if !ev0.HasError {
		t.Error("expected deny event to have has_error=true")
	}
	if ev0.ExitCode == nil || *ev0.ExitCode != 2 {
		t.Errorf("expected exit_code=2 for deny, got %v", ev0.ExitCode)
	}
	if ev0.Command != "Bash:exec" {
		t.Errorf("expected command 'Bash:exec', got %q", ev0.Command)
	}
	if ev0.Tags["outcome"] != "deny" {
		t.Errorf("expected tag outcome=deny, got %q", ev0.Tags["outcome"])
	}
	if ev0.Tags["reason"] != "Direct push to protected branch" {
		t.Errorf("unexpected reason tag: %q", ev0.Tags["reason"])
	}
	if ev0.AgentID != "claude-code" {
		t.Errorf("expected agent_id claude-code, got %q", ev0.AgentID)
	}

	// Second event: allow.
	ev1 := events[1]
	if ev1.HasError {
		t.Error("expected allow event to have has_error=false")
	}
	if ev1.ExitCode == nil || *ev1.ExitCode != 0 {
		t.Errorf("expected exit_code=0 for allow, got %v", ev1.ExitCode)
	}
	if ev1.Command != "Read:read" {
		t.Errorf("expected command 'Read:read', got %q", ev1.Command)
	}

	// Checkpoint should be set.
	if cp == nil {
		t.Fatal("expected non-nil checkpoint")
	}
	if cp.Adapter != "chitin_governance" {
		t.Errorf("expected adapter chitin_governance, got %q", cp.Adapter)
	}

	// Second ingest with checkpoint should return 0 events.
	events2, _, err := adapter.Ingest(context.Background(), cp)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if len(events2) != 0 {
		t.Errorf("expected 0 events on re-ingest, got %d", len(events2))
	}
}

// TestChitinGovernanceAdapter_TrustTelemetry verifies that trust_score /
// trust_level flow from chitin events into execution_events.tags. The
// zero-score case is important: chitin emits trust_score as *int precisely
// so "score = 0" (lowest-trust agent) survives omitempty serialization,
// and Sentinel must not drop that signal when forwarding to the tags map.
func TestChitinGovernanceAdapter_TrustTelemetry(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")
	data := `{"ts":"2026-04-12T09:00:00Z","sid":"s","agent":"a1","tool":"Bash","action":"exec","outcome":"allow","source":"policy","latency_us":100,"trust_score":500,"trust_level":"baseline"}
{"ts":"2026-04-12T09:01:00Z","sid":"s","agent":"a2","tool":"Bash","action":"exec","outcome":"deny","source":"policy","latency_us":100,"trust_score":0,"trust_level":"restricted"}
{"ts":"2026-04-12T09:02:00Z","sid":"s","agent":"a3","tool":"Bash","action":"exec","outcome":"allow","source":"policy","latency_us":100}
`
	os.WriteFile(eventsFile, []byte(data), 0644)

	adapter := NewChitinGovernanceAdapter([]string{dir})
	events, _, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	if got := events[0].Tags["trust_score"]; got != "500" {
		t.Errorf("baseline trust_score tag = %q, want %q", got, "500")
	}
	if got := events[0].Tags["trust_level"]; got != "baseline" {
		t.Errorf("baseline trust_level tag = %q, want %q", got, "baseline")
	}

	// The keystone assertion: score=0 must survive.
	if got := events[1].Tags["trust_score"]; got != "0" {
		t.Errorf("restricted trust_score tag = %q, want %q (score=0 must be preserved)", got, "0")
	}
	if got := events[1].Tags["trust_level"]; got != "restricted" {
		t.Errorf("restricted trust_level tag = %q, want %q", got, "restricted")
	}

	// Event without trust fields must not gain empty tags.
	if _, ok := events[2].Tags["trust_score"]; ok {
		t.Errorf("untagged event should not have trust_score tag, got %q", events[2].Tags["trust_score"])
	}
	if _, ok := events[2].Tags["trust_level"]; ok {
		t.Errorf("untagged event should not have trust_level tag, got %q", events[2].Tags["trust_level"])
	}
}

func TestChitinGovernanceAdapter_IncrementalRead(t *testing.T) {
	dir := t.TempDir()
	chitinDir := filepath.Join(dir, ".chitin")
	os.MkdirAll(chitinDir, 0755)

	eventsFile := filepath.Join(chitinDir, "events.jsonl")

	// Write 3 initial events.
	initial := `{"ts":"2026-04-11T19:00:00Z","sid":"s1","agent":"claude-code","tool":"Bash","action":"exec","outcome":"allow","reason":"","source":"policy","latency_us":100}
{"ts":"2026-04-11T19:01:00Z","sid":"s1","agent":"claude-code","tool":"Edit","action":"write","outcome":"allow","reason":"","source":"policy","latency_us":200}
{"ts":"2026-04-11T19:02:00Z","sid":"s1","agent":"claude-code","tool":"Bash","action":"exec","outcome":"deny","reason":"blocked","source":"invariant","latency_us":300}
`
	os.WriteFile(eventsFile, []byte(initial), 0644)

	adapter := NewChitinGovernanceAdapter([]string{dir})
	events1, cp1, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if len(events1) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events1))
	}

	// Append 2 more events.
	f, _ := os.OpenFile(eventsFile, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"ts":"2026-04-11T19:03:00Z","sid":"s2","agent":"copilot","tool":"Write","action":"write","outcome":"allow","reason":"","source":"policy","latency_us":150}
{"ts":"2026-04-11T19:04:00Z","sid":"s2","agent":"copilot","tool":"Bash","action":"exec","outcome":"deny","reason":"risky","source":"policy","latency_us":250}
`)
	f.Close()

	// Second ingest should return only the 2 new events.
	events2, _, err := adapter.Ingest(context.Background(), cp1)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if len(events2) != 2 {
		t.Errorf("expected 2 new events, got %d", len(events2))
	}
	if len(events2) > 0 && events2[0].AgentID != "copilot" {
		t.Errorf("expected copilot agent, got %q", events2[0].AgentID)
	}
}
