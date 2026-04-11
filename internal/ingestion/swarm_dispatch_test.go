package ingestion

import (
	"context"
	"os"
	"testing"
)

func TestSwarmDispatchAdapter_Ingest(t *testing.T) {
	f := createTempFile(t, `{"id":"swarm-1","timestamp":"2026-04-11T19:05:28Z","source":"swarm","session_id":"swarm-sess-1","sequence_num":1,"actor":"agent","agent_id":"claude","command":"swarm-intake","arguments":["--model","opus"],"exit_code":0,"duration_ms":1000,"repository":"chitinhq/chitin","has_error":false,"tags":{"platform":"claude","queue":"intake","result":"success"}}
{"id":"swarm-2","timestamp":"2026-04-11T19:06:00Z","source":"swarm","session_id":"swarm-sess-2","sequence_num":1,"actor":"agent","agent_id":"copilot","command":"swarm-review","exit_code":1,"duration_ms":2500,"repository":"chitinhq/octi","has_error":true,"tags":{"platform":"copilot","queue":"review","result":"failure"}}
{"id":"swarm-3","timestamp":"2026-04-11T19:07:00Z","source":"swarm","session_id":"swarm-sess-3","sequence_num":1,"actor":"agent","agent_id":"gemini","command":"swarm-groom","exit_code":0,"duration_ms":800,"repository":"chitinhq/sentinel","has_error":false,"tags":{"platform":"gemini","queue":"groom","result":"success"}}
`)

	adapter := NewSwarmDispatchAdapter(f)
	events, cp, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Check source override.
	for _, ev := range events {
		if ev.Source != SourceSwarmDispatch {
			t.Errorf("expected source swarm_dispatch, got %s", ev.Source)
		}
	}

	// Check field mapping.
	ev0 := events[0]
	if ev0.ID != "swarm-1" {
		t.Errorf("expected id swarm-1, got %q", ev0.ID)
	}
	if ev0.AgentID != "claude" {
		t.Errorf("expected agent_id claude, got %q", ev0.AgentID)
	}
	if ev0.Command != "swarm-intake" {
		t.Errorf("expected command swarm-intake, got %q", ev0.Command)
	}
	if ev0.Tags["platform"] != "claude" {
		t.Errorf("expected platform tag claude, got %q", ev0.Tags["platform"])
	}

	// Check error event.
	ev1 := events[1]
	if !ev1.HasError {
		t.Error("expected swarm-2 to have has_error=true")
	}
	if ev1.ExitCode == nil || *ev1.ExitCode != 1 {
		t.Errorf("expected exit_code=1, got %v", ev1.ExitCode)
	}

	// Checkpoint: second ingest returns 0 events.
	events2, _, err := adapter.Ingest(context.Background(), cp)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if len(events2) != 0 {
		t.Errorf("expected 0 events on re-ingest, got %d", len(events2))
	}
}

func TestSwarmDispatchAdapter_IncrementalRead(t *testing.T) {
	path := createTempFile(t, `{"id":"sw-1","timestamp":"2026-04-11T19:00:00Z","source":"swarm","session_id":"s1","sequence_num":1,"actor":"agent","agent_id":"claude","command":"intake","exit_code":0,"has_error":false,"tags":{}}
`)

	adapter := NewSwarmDispatchAdapter(path)
	events1, cp1, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("first Ingest: %v", err)
	}
	if len(events1) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events1))
	}

	// Append 2 more.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	f.WriteString(`{"id":"sw-2","timestamp":"2026-04-11T19:01:00Z","source":"swarm","session_id":"s2","sequence_num":1,"actor":"agent","agent_id":"copilot","command":"review","exit_code":0,"has_error":false,"tags":{}}
{"id":"sw-3","timestamp":"2026-04-11T19:02:00Z","source":"swarm","session_id":"s3","sequence_num":1,"actor":"agent","agent_id":"gemini","command":"groom","exit_code":0,"has_error":false,"tags":{}}
`)
	f.Close()

	events2, _, err := adapter.Ingest(context.Background(), cp1)
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if len(events2) != 2 {
		t.Errorf("expected 2 new events, got %d", len(events2))
	}
}

func createTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/swarm-events.jsonl"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
