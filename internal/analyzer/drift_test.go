package analyzer_test

import (
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
)

func TestDriftPass_DetectsActionDistributionDrift(t *testing.T) {
	now := time.Now()
	current := []analyzer.Event{
		{Action: "Bash", Timestamp: now},
		{Action: "Bash", Timestamp: now},
		{Action: "Bash", Timestamp: now}, // 60% Bash
		{Action: "Edit", Timestamp: now},
		{Action: "Edit", Timestamp: now}, // 40% Edit
	}
	baseline := []analyzer.Event{
		{Action: "Bash", Timestamp: now.Add(-24 * time.Hour)},
		{Action: "Bash", Timestamp: now.Add(-24 * time.Hour)}, // 40% Bash
		{Action: "Edit", Timestamp: now.Add(-24 * time.Hour)},
		{Action: "Edit", Timestamp: now.Add(-24 * time.Hour)},
		{Action: "Edit", Timestamp: now.Add(-24 * time.Hour)}, // 60% Edit
	}

	cfg := config.DriftConfig{
		ActionDistributionThreshold:   0.15, // 15% threshold
		OutcomeDistributionThreshold:  0.2,
		TemporalDistributionThreshold: 0.1,
	}

	findings := analyzer.DetectDrift(current, baseline, cfg)
	if len(findings) == 0 {
		t.Error("expected drift findings for action distribution change")
	}

	// Should detect Bash changed from 40% to 60% (20% change > 15% threshold)
	// Should detect Edit changed from 60% to 40% (20% change > 15% threshold)
	foundBash := false
	foundEdit := false
	for _, f := range findings {
		if f.PolicyID == "Bash" {
			foundBash = true
			if f.Metrics.Deviation < 0.19 || f.Metrics.Deviation > 0.21 {
				t.Errorf("expected ~0.20 deviation for Bash, got %f", f.Metrics.Deviation)
			}
		}
		if f.PolicyID == "Edit" {
			foundEdit = true
			if f.Metrics.Deviation < 0.19 || f.Metrics.Deviation > 0.21 {
				t.Errorf("expected ~0.20 deviation for Edit, got %f", f.Metrics.Deviation)
			}
		}
	}

	if !foundBash {
		t.Error("missing drift finding for Bash action distribution")
	}
	if !foundEdit {
		t.Error("missing drift finding for Edit action distribution")
	}
}

func TestDriftPass_DetectsOutcomeDistributionDrift(t *testing.T) {
	now := time.Now()
	current := []analyzer.Event{
		{Action: "Bash", Outcome: "deny", Timestamp: now},
		{Action: "Bash", Outcome: "deny", Timestamp: now},
		{Action: "Bash", Outcome: "deny", Timestamp: now}, // 75% denial rate
		{Action: "Bash", Outcome: "allow", Timestamp: now},
	}
	baseline := []analyzer.Event{
		{Action: "Bash", Outcome: "deny", Timestamp: now.Add(-24 * time.Hour)},
		{Action: "Bash", Outcome: "allow", Timestamp: now.Add(-24 * time.Hour)}, // 25% denial rate
		{Action: "Bash", Outcome: "allow", Timestamp: now.Add(-24 * time.Hour)},
		{Action: "Bash", Outcome: "allow", Timestamp: now.Add(-24 * time.Hour)},
	}

	cfg := config.DriftConfig{
		ActionDistributionThreshold:   0.1,
		OutcomeDistributionThreshold:  0.4, // 40% threshold
		TemporalDistributionThreshold: 0.1,
	}

	findings := analyzer.DetectDrift(current, baseline, cfg)
	if len(findings) == 0 {
		t.Error("expected drift findings for outcome distribution change")
	}

	// Should detect Bash denial rate changed from 25% to 75% (50% change > 40% threshold)
	foundBash := false
	for _, f := range findings {
		if f.PolicyID == "Bash" {
			foundBash = true
			if f.Metrics.Deviation < 0.49 || f.Metrics.Deviation > 0.51 {
				t.Errorf("expected ~0.50 deviation for Bash outcome, got %f", f.Metrics.Deviation)
			}
		}
	}

	if !foundBash {
		t.Error("missing drift finding for Bash outcome distribution")
	}
}

func TestDriftPass_NoDriftBelowThreshold(t *testing.T) {
	now := time.Now()
	// Same distribution in current and baseline
	events := []analyzer.Event{
		{Action: "Bash", Outcome: "allow", Timestamp: now},
		{Action: "Edit", Outcome: "allow", Timestamp: now},
		{Action: "Read", Outcome: "allow", Timestamp: now},
	}

	cfg := config.DriftConfig{
		ActionDistributionThreshold:   0.1,
		OutcomeDistributionThreshold:  0.1,
		TemporalDistributionThreshold: 0.1,
	}

	findings := analyzer.DetectDrift(events, events, cfg)
	if len(findings) != 0 {
		t.Errorf("expected no drift findings for identical distributions, got %d", len(findings))
	}
}

func TestDriftPass_EmptyEvents(t *testing.T) {
	cfg := config.DriftConfig{
		ActionDistributionThreshold:   0.1,
		OutcomeDistributionThreshold:  0.1,
		TemporalDistributionThreshold: 0.1,
	}

	// Test with empty current events
	findings := analyzer.DetectDrift(nil, []analyzer.Event{{Action: "Bash"}}, cfg)
	if len(findings) != 0 {
		t.Errorf("expected no findings with empty current events, got %d", len(findings))
	}

	// Test with empty baseline events
	findings = analyzer.DetectDrift([]analyzer.Event{{Action: "Bash"}}, nil, cfg)
	if len(findings) != 0 {
		t.Errorf("expected no findings with empty baseline events, got %d", len(findings))
	}

	// Test with both empty
	findings = analyzer.DetectDrift(nil, nil, cfg)
	if len(findings) != 0 {
		t.Errorf("expected no findings with both empty events, got %d", len(findings))
	}
}