package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
	"github.com/AgentGuardHQ/sentinel/internal/pipeline"
)

// --- Mock store ------------------------------------------------------------

// mockStore implements analyzer.Store and returns canned data.
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

func (m *mockStore) Close() {}

// --- Mock interpreter ------------------------------------------------------

// passthroughInterpreter returns zero-confidence InterpretedFindings for all inputs.
type passthroughInterpreter struct{}

func (p *passthroughInterpreter) Interpret(_ context.Context, findings []analyzer.Finding) ([]analyzer.InterpretedFinding, error) {
	out := make([]analyzer.InterpretedFinding, len(findings))
	for i, f := range findings {
		out[i] = analyzer.InterpretedFinding{
			Finding:    f,
			Confidence: 0.0,
		}
	}
	return out, nil
}

// --- Mock memory client ----------------------------------------------------

type mockMemory struct{}

func (m *mockMemory) Store(_ context.Context, _ string, _ []string, _ string) (string, error) {
	return "mock-id", nil
}

func (m *mockMemory) Recall(_ context.Context, _ string, _ int) ([]memory.MemoryEntry, error) {
	return nil, nil
}

// --- Mock GitHub client ----------------------------------------------------

type mockGitHub struct{}

func (g *mockGitHub) SearchIssues(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (g *mockGitHub) CreateIssue(_ context.Context, _ analyzer.InterpretedFinding, _ string, _ []string) (string, error) {
	return "https://github.com/mock/repo/issues/1", nil
}

// --- Tests -----------------------------------------------------------------

func testConfig() *config.Config {
	return &config.Config{
		Analysis: config.AnalysisConfig{
			Lookback:    24 * time.Hour,
			TrendWindow: 7 * 24 * time.Hour,
		},
		Detection: config.DetectionConfig{
			FalsePositive: config.FalsePositiveConfig{
				MinSampleSize:         5,
				DeviationThreshold:    2.0,
				AbsoluteRateThreshold: 0.5,
			},
			Bypass: config.BypassConfig{
				Window:     5 * time.Minute,
				MinRetries: 3,
			},
			Drift: config.DriftConfig{
				ActionDistributionThreshold:   0.1,
				OutcomeDistributionThreshold:  0.15,
				TemporalDistributionThreshold: 0.05,
			},
		},
		Routing: config.RoutingConfig{
			HighConfidence:   0.75,
			MediumConfidence: 0.40,
			DedupSimilarity:  0.85,
			DedupLookback:    7 * 24 * time.Hour,
		},
		Interpreter: config.InterpreterConfig{
			Model:               "claude-sonnet-4-6",
			MaxFindingsPerBatch: 20,
		},
		GitHub: config.GitHubConfig{
			Repo:   "mock/repo",
			Labels: []string{"sentinel"},
		},
	}
}

// TestAnalyze_EmptyStore verifies that an empty store produces a non-nil
// result with zero findings.
func TestAnalyze_EmptyStore(t *testing.T) {
	store := &mockStore{}
	cfg := testConfig()
	p := pipeline.New(cfg, store, &passthroughInterpreter{}, &mockMemory{}, &mockGitHub{})

	result, err := p.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Analyze returned nil result")
	}
	if result.TotalFindings != 0 {
		t.Errorf("expected 0 findings, got %d", result.TotalFindings)
	}
}

// TestAnalyze_WithHotspots verifies that a store with high-action-count data
// produces at least one finding and a non-nil result.
func TestAnalyze_WithHotspots(t *testing.T) {
	store := &mockStore{
		// Provide action counts that should trigger hotspot detection.
		actionCounts: []db.ActionCount{
			{Action: "Bash", Outcome: "allow", Count: 500},
			{Action: "Edit", Outcome: "deny", Count: 200},
			{Action: "Read", Outcome: "allow", Count: 800},
		},
		denialRates: []db.DenialRate{
			{Action: "Edit", TotalCount: 300, DenialCount: 200, DenialRate: 0.667},
		},
	}

	cfg := testConfig()
	p := pipeline.New(cfg, store, &passthroughInterpreter{}, &mockMemory{}, &mockGitHub{})

	result, err := p.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Analyze returned nil result")
	}
	if result.TotalFindings == 0 {
		t.Error("expected at least one finding from action count data, got zero")
	}

	// Interpreted and Decisions slices must be parallel.
	if len(result.Interpreted) != len(result.Decisions) {
		t.Errorf("interpreted len %d != decisions len %d",
			len(result.Interpreted), len(result.Decisions))
	}
}

// TestAnalyze_ConfidenceCounts verifies that high-/medium-/low-confidence
// counters sum to TotalFindings.
func TestAnalyze_ConfidenceCounts(t *testing.T) {
	store := &mockStore{
		actionCounts: []db.ActionCount{
			{Action: "Bash", Outcome: "allow", Count: 1000},
		},
		denialRates: []db.DenialRate{
			{Action: "Bash", TotalCount: 100, DenialCount: 80, DenialRate: 0.8},
		},
	}

	cfg := testConfig()
	p := pipeline.New(cfg, store, &passthroughInterpreter{}, &mockMemory{}, &mockGitHub{})

	result, err := p.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}

	total := result.HighConfidence + result.MediumConfidence + result.LowConfidence
	if total != result.TotalFindings {
		t.Errorf("confidence counts sum %d != TotalFindings %d", total, result.TotalFindings)
	}
}

// TestAnalyze_IssueURLs verifies that IssueURLs is always non-nil.
func TestAnalyze_IssueURLs(t *testing.T) {
	store := &mockStore{}
	cfg := testConfig()
	p := pipeline.New(cfg, store, &passthroughInterpreter{}, &mockMemory{}, &mockGitHub{})

	result, err := p.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze returned error: %v", err)
	}
	if result.IssueURLs == nil {
		t.Error("IssueURLs should never be nil")
	}
}
