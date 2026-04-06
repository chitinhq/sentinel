package ingestion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
)

func TestIngestRunParsing(t *testing.T) {
	// Two steps: step 0 succeeds, step 1 fails.
	now := time.Now().UTC().Truncate(time.Second)
	runsPayload := ghRunsResponse{
		WorkflowRuns: []ghRun{
			{
				ID:         42,
				Name:       "CI",
				HeadBranch: "main",
				Actor:      ghActor{Login: "octi-pulpo"},
			},
		},
	}
	jobsPayload := ghJobsResponse{
		Jobs: []ghJob{
			{
				ID:   7,
				Name: "build",
				Steps: []ghStep{
					{Name: "checkout", Conclusion: "success", StartedAt: now},
					{Name: "test", Conclusion: "failure", StartedAt: now.Add(time.Second)},
				},
			},
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/org/repo/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runsPayload)
	})
	mux.HandleFunc("/repos/org/repo/actions/runs/42/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jobsPayload)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.GitHubActionsConfig{
		Repos: []string{"org/repo"},
		Since: 168 * time.Hour,
		ActorPatterns: []config.ActorPatternConfig{
			{Pattern: "octi-pulpo", AgentID: "octi-pulpo"},
		},
	}
	adapter := NewGHActionsAdapter(cfg, srv.URL, "test-token")

	events, err := adapter.Ingest(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ingest returned error: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Step 0: checkout — success
	e0 := events[0]
	if e0.ID != "gha-42-7-0" {
		t.Errorf("event 0 ID: want gha-42-7-0, got %s", e0.ID)
	}
	if e0.HasError {
		t.Errorf("event 0 should not have error")
	}
	if *e0.ExitCode != 0 {
		t.Errorf("event 0 exit code: want 0, got %d", *e0.ExitCode)
	}
	if e0.Actor != ActorAgent {
		t.Errorf("event 0 actor: want agent, got %s", e0.Actor)
	}
	if e0.AgentID != "octi-pulpo" {
		t.Errorf("event 0 agentID: want octi-pulpo, got %s", e0.AgentID)
	}

	// Step 1: test — failure
	e1 := events[1]
	if e1.ID != "gha-42-7-1" {
		t.Errorf("event 1 ID: want gha-42-7-1, got %s", e1.ID)
	}
	if !e1.HasError {
		t.Errorf("event 1 should have error")
	}
	if *e1.ExitCode != 1 {
		t.Errorf("event 1 exit code: want 1, got %d", *e1.ExitCode)
	}
}

func TestClassifyActor(t *testing.T) {
	patterns := []config.ActorPatternConfig{
		{Pattern: `github-actions\[bot\]`, AgentID: "github-actions"},
		{Pattern: "copilot", AgentID: "copilot"},
		{Pattern: "octi-pulpo", AgentID: "octi-pulpo"},
	}

	tests := []struct {
		login        string
		wantType     ActorType
		wantAgentID  string
	}{
		{"jpleva91", ActorHuman, ""},
		{"github-actions[bot]", ActorAgent, "github-actions"},
		{"copilot-agent", ActorAgent, "copilot"},
		{"octi-pulpo", ActorAgent, "octi-pulpo"},
		{"dependabot[bot]", ActorAgent, "dependabot[bot]"},
	}

	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			gotType, gotAgentID := classifyActor(tt.login, patterns)
			if gotType != tt.wantType {
				t.Errorf("classifyActor(%q) type: want %s, got %s", tt.login, tt.wantType, gotType)
			}
			if gotAgentID != tt.wantAgentID {
				t.Errorf("classifyActor(%q) agentID: want %q, got %q", tt.login, tt.wantAgentID, gotAgentID)
			}
		})
	}
}
