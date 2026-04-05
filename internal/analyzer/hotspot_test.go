package analyzer_test

import (
	"testing"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/db"
)

func TestHotspotPass_RanksActionsByVolume(t *testing.T) {
	counts := []db.ActionCount{
		{Action: "Bash", Outcome: "deny", Count: 50},
		{Action: "Bash", Outcome: "allow", Count: 200},
		{Action: "Edit", Outcome: "deny", Count: 10},
		{Action: "Edit", Outcome: "allow", Count: 100},
		{Action: "Read", Outcome: "allow", Count: 500},
	}

	findings := analyzer.DetectHotspots(counts)

	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}
	if findings[0].PolicyID != "Bash" {
		t.Errorf("top hotspot = %s, want Bash", findings[0].PolicyID)
	}
	if findings[0].Pass != "hotspot" {
		t.Errorf("pass = %s, want hotspot", findings[0].Pass)
	}
	if findings[0].Metrics.Count != 50 {
		t.Errorf("denial count = %d, want 50", findings[0].Metrics.Count)
	}
}

func TestHotspotPass_SkipsZeroDenials(t *testing.T) {
	counts := []db.ActionCount{
		{Action: "Read", Outcome: "allow", Count: 500},
	}
	findings := analyzer.DetectHotspots(counts)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for no denials, got %d", len(findings))
	}
}
