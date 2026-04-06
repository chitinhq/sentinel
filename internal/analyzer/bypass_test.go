package analyzer_test

import (
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/config"
)

func TestBypassPass_DetectsRetryPattern(t *testing.T) {
	now := time.Now()
	events := []analyzer.Event{
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now},
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(30 * time.Second)},
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(60 * time.Second)},
		{AgentID: "agent-2", Action: "Bash", Outcome: "deny", Timestamp: now.Add(10 * time.Second)},
	}
	cfg := config.BypassConfig{Window: 5 * time.Minute, MinRetries: 2}
	findings := analyzer.DetectBypassPatterns(events, cfg)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].PolicyID != "Bash" {
		t.Errorf("action = %s, want Bash", findings[0].PolicyID)
	}
	if findings[0].Metrics.Count != 3 {
		t.Errorf("retry count = %d, want 3", findings[0].Metrics.Count)
	}
}

func TestBypassPass_IgnoresBelowThreshold(t *testing.T) {
	now := time.Now()
	events := []analyzer.Event{
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now},
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(30 * time.Second)},
	}
	cfg := config.BypassConfig{Window: 5 * time.Minute, MinRetries: 3}
	findings := analyzer.DetectBypassPatterns(events, cfg)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings below threshold, got %d", len(findings))
	}
}

func TestBypassPass_SplitsAcrossWindows(t *testing.T) {
	now := time.Now()
	events := []analyzer.Event{
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now},
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(30 * time.Second)},
		{AgentID: "agent-1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(10 * time.Minute)},
	}
	cfg := config.BypassConfig{Window: 5 * time.Minute, MinRetries: 2}
	findings := analyzer.DetectBypassPatterns(events, cfg)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings across windows, got %d", len(findings))
	}
}
