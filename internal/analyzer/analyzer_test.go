package analyzer_test

import (
	"context"
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

type mockStore struct {
	events         []analyzer.Event
	actionCounts   []db.ActionCount
	denialRates    []db.DenialRate
	sessionDenials []db.SessionDenialCount
	hourlyVolumes  []db.HourlyVolume
}

func (m *mockStore) QueryEvents(_ context.Context, _, _ time.Time) ([]analyzer.Event, error) {
	return m.events, nil
}
func (m *mockStore) QueryActionCounts(_ context.Context, _ time.Time) ([]db.ActionCount, error) {
	return m.actionCounts, nil
}
func (m *mockStore) QueryDenialRates(_ context.Context, _ time.Time) ([]db.DenialRate, error) {
	return m.denialRates, nil
}
func (m *mockStore) QuerySessionDenials(_ context.Context, _ time.Time) ([]db.SessionDenialCount, error) {
	return m.sessionDenials, nil
}
func (m *mockStore) QueryHourlyVolumes(_ context.Context, _ time.Time) ([]db.HourlyVolume, error) {
	return m.hourlyVolumes, nil
}
func (m *mockStore) QueryCommandFailureRates(_ context.Context, _ time.Time) ([]db.CommandFailureRate, error) {
	return nil, nil
}
func (m *mockStore) QuerySessionSequences(_ context.Context, _ time.Time) ([]db.SessionSequence, error) {
	return nil, nil
}
func (m *mockStore) Close() {}

func TestAnalyzer_RunAllPasses(t *testing.T) {
	now := time.Now()
	store := &mockStore{
		events: []analyzer.Event{
			{AgentID: "a1", Action: "Bash", Outcome: "deny", Timestamp: now},
			{AgentID: "a1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(10 * time.Second)},
			{AgentID: "a1", Action: "Bash", Outcome: "deny", Timestamp: now.Add(20 * time.Second)},
		},
		actionCounts: []db.ActionCount{
			{Action: "Bash", Outcome: "deny", Count: 30},
			{Action: "Bash", Outcome: "allow", Count: 70},
		},
		denialRates: []db.DenialRate{
			{Action: "Bash", TotalCount: 100, DenialCount: 30, DenialRate: 0.3},
		},
		sessionDenials: []db.SessionDenialCount{{SessionID: "s1", AgentID: "a1", Denials: 3, Total: 5}},
		hourlyVolumes:  []db.HourlyVolume{{Hour: now.Truncate(time.Hour), Count: 10}},
	}
	cfg := config.Config{
		Analysis: config.AnalysisConfig{Lookback: 24 * time.Hour, TrendWindow: 168 * time.Hour},
		Detection: config.DetectionConfig{
			FalsePositive: config.FalsePositiveConfig{MinSampleSize: 20, DeviationThreshold: 2.0, AbsoluteRateThreshold: 0.3},
			Bypass:        config.BypassConfig{Window: 5 * time.Minute, MinRetries: 2},
			Anomaly:       config.AnomalyConfig{VolumeSpikeThreshold: 3.0},
		},
	}
	a := analyzer.New(store, &cfg)
	findings, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(findings) == 0 {
		t.Error("expected findings from analyzer")
	}
	passes := make(map[string]bool)
	for _, f := range findings {
		passes[f.Pass] = true
	}
	if !passes["hotspot"] {
		t.Error("missing hotspot findings")
	}
}
