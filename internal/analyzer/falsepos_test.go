package analyzer_test

import (
	"testing"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
)

func TestFalsePositivePass_DetectsHighDenialRate(t *testing.T) {
	current := []db.DenialRate{
		{Action: "Bash", TotalCount: 100, DenialCount: 40, DenialRate: 0.4},
		{Action: "Edit", TotalCount: 50, DenialCount: 5, DenialRate: 0.1},
	}
	baseline := []db.DenialRate{
		{Action: "Bash", TotalCount: 700, DenialCount: 70, DenialRate: 0.1},
		{Action: "Edit", TotalCount: 350, DenialCount: 35, DenialRate: 0.1},
	}
	cfg := config.FalsePositiveConfig{
		MinSampleSize: 20, DeviationThreshold: 2.0, AbsoluteRateThreshold: 0.3,
	}
	findings := analyzer.DetectFalsePositives(current, baseline, cfg)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if findings[0].PolicyID != "Bash" {
		t.Errorf("finding action = %s, want Bash", findings[0].PolicyID)
	}
	if findings[0].Pass != "false_positive" {
		t.Errorf("pass = %s, want false_positive", findings[0].Pass)
	}
}

func TestFalsePositivePass_SkipsLowSampleSize(t *testing.T) {
	current := []db.DenialRate{{Action: "Bash", TotalCount: 5, DenialCount: 3, DenialRate: 0.6}}
	baseline := []db.DenialRate{{Action: "Bash", TotalCount: 35, DenialCount: 3, DenialRate: 0.086}}
	cfg := config.FalsePositiveConfig{
		MinSampleSize: 20, DeviationThreshold: 2.0, AbsoluteRateThreshold: 0.3,
	}
	findings := analyzer.DetectFalsePositives(current, baseline, cfg)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for low sample size, got %d", len(findings))
	}
}
