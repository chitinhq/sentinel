package ingestion

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExecutionEventJSON(t *testing.T) {
	ts := time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC)
	exitCode := 0
	dur := int64(1500)
	ev := ExecutionEvent{
		ID:          "gh-run-123-step-1",
		Timestamp:   ts,
		Source:      SourceGitHubActions,
		SessionID:   "run-123",
		SequenceNum: 1,
		Actor:       ActorAgent,
		AgentID:     "copilot",
		Command:     "npm test",
		Arguments:   []string{"test"},
		ExitCode:    &exitCode,
		DurationMs:  &dur,
		Repository:  "chitinhq/agent-guard",
		Branch:      "main",
		HasError:    false,
		Tags:        map[string]string{"workflow": "ci"},
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ExecutionEvent
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ID != ev.ID {
		t.Errorf("ID = %q, want %q", got.ID, ev.ID)
	}
	if got.Source != SourceGitHubActions {
		t.Errorf("Source = %q, want %q", got.Source, SourceGitHubActions)
	}
	if got.Actor != ActorAgent {
		t.Errorf("Actor = %q, want %q", got.Actor, ActorAgent)
	}
	if *got.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", *got.ExitCode)
	}
}

func TestEventSourceValues(t *testing.T) {
	sources := []EventSource{SourceGitHubActions, SourceShellHistory, SourceTermius}
	want := []string{"github_actions", "shell_history", "termius"}
	for i, s := range sources {
		if string(s) != want[i] {
			t.Errorf("source[%d] = %q, want %q", i, s, want[i])
		}
	}
}

func TestActorTypeValues(t *testing.T) {
	actors := []ActorType{ActorHuman, ActorAgent, ActorUnknown}
	want := []string{"human", "agent", "unknown"}
	for i, a := range actors {
		if string(a) != want[i] {
			t.Errorf("actor[%d] = %q, want %q", i, a, want[i])
		}
	}
}
