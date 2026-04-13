package flow

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmit_WritesLifecycleEvents(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "events.jsonl")
	t.Setenv("FLOW_EVENTS_FILE", dest)
	t.Setenv("CHITIN_SESSION_ID", "sess-1")
	t.Setenv("CHITIN_AGENT_NAME", "test-agent")

	Emit("sentinel.analyze", Started, nil)
	Emit("sentinel.analyze", Completed, map[string]any{"findings": 3})
	Emit("chitin.hook.pretool", Failed, map[string]any{"reason": "fail-closed"})

	f, err := os.Open(dest)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	var events []event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("parse: %v", err)
		}
		events = append(events, ev)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}

	if events[0].Tool != "flow.sentinel.analyze" || events[0].Action != "flow_started" || events[0].Outcome != "allow" {
		t.Errorf("started event shape wrong: %+v", events[0])
	}
	if events[1].Action != "flow_completed" {
		t.Errorf("completed action: got %q", events[1].Action)
	}
	if events[2].Action != "flow_failed" || events[2].Outcome != "deny" || events[2].Reason != "fail-closed" {
		t.Errorf("failed event shape wrong: %+v", events[2])
	}
	for _, ev := range events {
		if ev.SessionID != "sess-1" {
			t.Errorf("session id not plumbed: %+v", ev)
		}
		if ev.Agent != "test-agent" {
			t.Errorf("agent not plumbed: %+v", ev)
		}
		if ev.Source != "flow" {
			t.Errorf("source should be flow: %+v", ev)
		}
	}
}

func TestSpan_CompletesOnNoError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_EVENTS_FILE", filepath.Join(dir, "events.jsonl"))

	err := Span("my.flow", nil, func() error { return nil })
	if err != nil {
		t.Fatalf("Span returned error unexpectedly: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(data), `"action":"flow_started"`) {
		t.Error("Span should have emitted a started event")
	}
	if !strings.Contains(string(data), `"action":"flow_completed"`) {
		t.Error("Span should have emitted a completed event on success")
	}
	if strings.Contains(string(data), `"action":"flow_failed"`) {
		t.Error("Span should NOT emit a failed event on success")
	}
}

func TestSpan_FailsOnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLOW_EVENTS_FILE", filepath.Join(dir, "events.jsonl"))

	err := Span("my.flow", map[string]any{"context": "test"}, func() error {
		return errors.New("kaboom")
	})
	if err == nil || err.Error() != "kaboom" {
		t.Fatalf("Span should return the inner error unchanged, got %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if !strings.Contains(string(data), `"action":"flow_failed"`) {
		t.Error("Span should emit a failed event when fn returns error")
	}
	if !strings.Contains(string(data), `"reason":"kaboom"`) {
		t.Error("fail reason should be the error message")
	}
	// Metadata from fields should flow through to the emitted event.
	if !strings.Contains(string(data), `"context":"test"`) {
		t.Error("user fields should be preserved in failure emission")
	}
}

func TestEmit_NoDestinationIsNoop(t *testing.T) {
	t.Setenv("FLOW_EVENTS_FILE", "")
	t.Setenv("MCPTRACE_FILE", "")
	t.Setenv("CHITIN_WORKSPACE", "")
	// HOME fallback may still resolve; this test just verifies no panic.
	Emit("my.flow", Started, nil)
}

func TestDestination_Precedence(t *testing.T) {
	t.Setenv("FLOW_EVENTS_FILE", "/tmp/flow.jsonl")
	t.Setenv("MCPTRACE_FILE", "/tmp/mcp.jsonl")
	t.Setenv("CHITIN_WORKSPACE", "/tmp/ws")
	if got := destination(); got != "/tmp/flow.jsonl" {
		t.Errorf("FLOW_EVENTS_FILE should win, got %q", got)
	}

	t.Setenv("FLOW_EVENTS_FILE", "")
	if got := destination(); got != "/tmp/mcp.jsonl" {
		t.Errorf("MCPTRACE_FILE should win over CHITIN_WORKSPACE, got %q", got)
	}

	t.Setenv("MCPTRACE_FILE", "")
	if got := destination(); got != "/tmp/ws/.chitin/events.jsonl" {
		t.Errorf("CHITIN_WORKSPACE should win over HOME, got %q", got)
	}
}
