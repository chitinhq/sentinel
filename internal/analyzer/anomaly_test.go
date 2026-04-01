package analyzer_test

import (
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
)

func TestAnomalyPass_DetectsVolumeSpike(t *testing.T) {
	now := time.Now().Truncate(time.Hour)
	volumes := []db.HourlyVolume{
		{Hour: now.Add(-6 * time.Hour), Count: 10},
		{Hour: now.Add(-5 * time.Hour), Count: 12},
		{Hour: now.Add(-4 * time.Hour), Count: 11},
		{Hour: now.Add(-3 * time.Hour), Count: 9},
		{Hour: now.Add(-2 * time.Hour), Count: 10},
		{Hour: now.Add(-1 * time.Hour), Count: 50},
	}
	cfg := config.AnomalyConfig{VolumeSpikeThreshold: 3.0}
	findings := analyzer.DetectAnomalies(volumes, nil, cfg)
	found := false
	for _, f := range findings {
		if f.Pass == "anomaly" {
			found = true
		}
	}
	if !found {
		t.Error("expected volume spike anomaly finding")
	}
}

func TestAnomalyPass_DetectsHighDenialSession(t *testing.T) {
	sessions := []db.SessionDenialCount{
		{SessionID: "s1", AgentID: "a1", Denials: 25, Total: 30},
		{SessionID: "s2", AgentID: "a2", Denials: 2, Total: 50},
		{SessionID: "s3", AgentID: "a3", Denials: 3, Total: 40},
	}
	cfg := config.AnomalyConfig{VolumeSpikeThreshold: 3.0}
	findings := analyzer.DetectAnomalies(nil, sessions, cfg)
	found := false
	for _, f := range findings {
		if f.PolicyID == "session:s1" {
			found = true
		}
	}
	if !found {
		t.Error("expected high denial session anomaly")
	}
}

func TestAnomalyPass_NoAnomalies(t *testing.T) {
	volumes := []db.HourlyVolume{
		{Hour: time.Now(), Count: 10},
		{Hour: time.Now().Add(-time.Hour), Count: 11},
	}
	cfg := config.AnomalyConfig{VolumeSpikeThreshold: 3.0}
	findings := analyzer.DetectAnomalies(volumes, nil, cfg)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}
