package analyzer_test

import (
	"testing"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/db"
)

func TestToolRiskPass_ProfilesTools(t *testing.T) {
	rates := []db.DenialRate{
		{Action: "Bash", TotalCount: 200, DenialCount: 60, DenialRate: 0.3},
		{Action: "Edit", TotalCount: 100, DenialCount: 5, DenialRate: 0.05},
		{Action: "Read", TotalCount: 500, DenialCount: 0, DenialRate: 0.0},
	}
	findings := analyzer.ProfileToolRisk(rates)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}
	if findings[0].PolicyID != "Bash" {
		t.Errorf("top risk tool = %s, want Bash", findings[0].PolicyID)
	}
}
