package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
	"github.com/AgentGuardHQ/sentinel/internal/router"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockMemory struct{ entries []memory.MemoryEntry }

func (m *mockMemory) Store(_ context.Context, _ string, _ []string, _ string) (string, error) {
	return "mem-new", nil
}
func (m *mockMemory) Recall(_ context.Context, _ string, _ int) ([]memory.MemoryEntry, error) {
	return m.entries, nil
}

type mockGitHub struct{ issues []string }

func (m *mockGitHub) SearchIssues(_ context.Context, _ string) ([]string, error) {
	return m.issues, nil
}
func (m *mockGitHub) CreateIssue(_ context.Context, _ analyzer.InterpretedFinding, _ string, _ []string) (string, error) {
	return "https://github.com/test/repo/issues/1", nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRouter_HighConfidenceCreatesIssue(t *testing.T) {
	cfg := config.RoutingConfig{HighConfidence: 0.8, MediumConfidence: 0.5}
	r := router.New(cfg, &mockMemory{}, &mockGitHub{}, config.GitHubConfig{Repo: "test/repo"})
	finding := analyzer.InterpretedFinding{
		Finding:    analyzer.Finding{ID: "f1", Pass: "hotspot", PolicyID: "Bash", DetectedAt: time.Now()},
		Actionable: true,
		Confidence: 0.85,
	}
	decision, err := r.Route(context.Background(), finding)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if !decision.GitHubIssue {
		t.Error("expected GitHubIssue = true")
	}
	if !decision.Qdrant {
		t.Error("expected Qdrant = true")
	}
}

func TestRouter_MediumConfidenceGoesToDigest(t *testing.T) {
	cfg := config.RoutingConfig{HighConfidence: 0.8, MediumConfidence: 0.5}
	r := router.New(cfg, &mockMemory{}, &mockGitHub{}, config.GitHubConfig{})
	finding := analyzer.InterpretedFinding{
		Finding:    analyzer.Finding{ID: "f1", DetectedAt: time.Now()},
		Confidence: 0.65,
	}
	decision, err := r.Route(context.Background(), finding)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if !decision.WeeklyDigest {
		t.Error("expected WeeklyDigest = true")
	}
	if decision.GitHubIssue {
		t.Error("expected GitHubIssue = false")
	}
}

func TestRouter_LowConfidenceQdrantOnly(t *testing.T) {
	cfg := config.RoutingConfig{HighConfidence: 0.8, MediumConfidence: 0.5}
	r := router.New(cfg, &mockMemory{}, &mockGitHub{}, config.GitHubConfig{})
	finding := analyzer.InterpretedFinding{
		Finding:    analyzer.Finding{ID: "f1", DetectedAt: time.Now()},
		Confidence: 0.3,
	}
	decision, err := r.Route(context.Background(), finding)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if decision.GitHubIssue || decision.WeeklyDigest {
		t.Error("expected Qdrant-only")
	}
	if !decision.Qdrant {
		t.Error("expected Qdrant = true")
	}
}

func TestRouter_DuplicateSkipsIssue(t *testing.T) {
	cfg := config.RoutingConfig{HighConfidence: 0.8, MediumConfidence: 0.5}
	gh := &mockGitHub{issues: []string{"existing-issue"}}
	r := router.New(cfg, &mockMemory{}, gh, config.GitHubConfig{})
	finding := analyzer.InterpretedFinding{
		Finding:    analyzer.Finding{ID: "f1", Pass: "hotspot", PolicyID: "Bash", DetectedAt: time.Now()},
		Actionable: true,
		Confidence: 0.9,
	}
	decision, err := r.Route(context.Background(), finding)
	if err != nil {
		t.Fatalf("Route() error: %v", err)
	}
	if decision.GitHubIssue {
		t.Error("expected no issue for duplicate")
	}
	if !decision.IsDuplicate {
		t.Error("expected IsDuplicate")
	}
}

func TestRouteAll_BatchProcessing(t *testing.T) {
	cfg := config.RoutingConfig{HighConfidence: 0.8, MediumConfidence: 0.5}
	r := router.New(cfg, &mockMemory{}, &mockGitHub{}, config.GitHubConfig{Repo: "test/repo"})
	findings := []analyzer.InterpretedFinding{
		{Finding: analyzer.Finding{ID: "a", DetectedAt: time.Now()}, Confidence: 0.9, Actionable: true},
		{Finding: analyzer.Finding{ID: "b", DetectedAt: time.Now()}, Confidence: 0.6},
		{Finding: analyzer.Finding{ID: "c", DetectedAt: time.Now()}, Confidence: 0.2},
	}
	decisions, err := r.RouteAll(context.Background(), findings)
	if err != nil {
		t.Fatalf("RouteAll() error: %v", err)
	}
	if len(decisions) != 3 {
		t.Fatalf("expected 3 decisions, got %d", len(decisions))
	}
	if !decisions[0].GitHubIssue {
		t.Error("[0] expected GitHubIssue")
	}
	if !decisions[1].WeeklyDigest {
		t.Error("[1] expected WeeklyDigest")
	}
	if decisions[2].GitHubIssue || decisions[2].WeeklyDigest {
		t.Error("[2] expected Qdrant-only")
	}
}
