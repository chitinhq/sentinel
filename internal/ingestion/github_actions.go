package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/config"
)

// GHActionsAdapter fetches GitHub Actions workflow runs and parses job steps
// into ExecutionEvents.
type GHActionsAdapter struct {
	cfg     config.GitHubActionsConfig
	apiBase string
	token   string
	client  *http.Client
}

// NewGHActionsAdapter constructs a GHActionsAdapter.
func NewGHActionsAdapter(cfg config.GitHubActionsConfig, apiBase, token string) *GHActionsAdapter {
	return &GHActionsAdapter{
		cfg:     cfg,
		apiBase: strings.TrimRight(apiBase, "/"),
		token:   token,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Ingest fetches workflow runs for all configured repos since the checkpoint
// (or cfg.Since if no checkpoint is set) and returns ExecutionEvents.
func (a *GHActionsAdapter) Ingest(ctx context.Context, cp *Checkpoint) ([]ExecutionEvent, error) {
	since := time.Now().Add(-a.cfg.Since)
	if cp != nil && !cp.LastRunAt.IsZero() {
		since = cp.LastRunAt
	}

	var all []ExecutionEvent
	for _, repo := range a.cfg.Repos {
		events, err := a.ingestRepo(ctx, repo, since)
		if err != nil {
			// Log and continue — one repo failure should not abort others.
			continue
		}
		all = append(all, events...)
	}
	return all, nil
}

// ingestRepo fetches workflow runs from a single repo and converts them to events.
func (a *GHActionsAdapter) ingestRepo(ctx context.Context, repo string, since time.Time) ([]ExecutionEvent, error) {
	runs, err := a.listRuns(ctx, repo, since)
	if err != nil {
		return nil, fmt.Errorf("list runs for %s: %w", repo, err)
	}

	var events []ExecutionEvent
	for _, run := range runs {
		jobs, err := a.listJobs(ctx, repo, run.ID)
		if err != nil {
			continue
		}
		for _, job := range jobs {
			actorType, agentID := classifyActor(run.Actor.Login, a.cfg.ActorPatterns)
			jobEvents := stepsToEvents(run, job, repo, actorType, agentID)
			events = append(events, jobEvents...)
		}
	}
	return events, nil
}

// stepsToEvents converts a job's steps into ExecutionEvents.
func stepsToEvents(run ghRun, job ghJob, repo string, actorType ActorType, agentID string) []ExecutionEvent {
	var events []ExecutionEvent
	for seq, step := range job.Steps {
		hasError := step.Conclusion == "failure"
		exitCode := 0
		if hasError {
			exitCode = 1
		}

		ev := ExecutionEvent{
			ID:          fmt.Sprintf("gha-%d-%d-%d", run.ID, job.ID, seq),
			Timestamp:   step.StartedAt,
			Source:      SourceGitHubActions,
			SessionID:   fmt.Sprintf("gha-%d-%d", run.ID, job.ID),
			SequenceNum: seq,
			Actor:       actorType,
			AgentID:     agentID,
			Command:     step.Name,
			ExitCode:    &exitCode,
			Repository:  repo,
			Branch:      run.HeadBranch,
			HasError:    hasError,
			Tags: map[string]string{
				"workflow":   run.Name,
				"job":        job.Name,
				"conclusion": step.Conclusion,
				"run_id":     fmt.Sprintf("%d", run.ID),
			},
		}
		events = append(events, ev)
	}
	return events
}

// classifyActor determines whether a GitHub login is a human, agent, or unknown,
// and returns the matching agent_id when applicable.
func classifyActor(login string, patterns []config.ActorPatternConfig) (ActorType, string) {
	for _, p := range patterns {
		matched, err := regexp.MatchString(p.Pattern, login)
		if err == nil && matched {
			return ActorAgent, p.AgentID
		}
	}
	// GitHub bot accounts contain "[bot]"
	if strings.Contains(login, "[bot]") {
		return ActorAgent, login
	}
	return ActorHuman, ""
}

// --- GitHub API response types -----------------------------------------------

type ghRunsResponse struct {
	WorkflowRuns []ghRun `json:"workflow_runs"`
}

type ghRun struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	HeadBranch string  `json:"head_branch"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Actor      ghActor `json:"actor"`
	Repository ghRepo  `json:"repository"`
}

type ghRepo struct {
	FullName string `json:"full_name"`
}

type ghActor struct {
	Login string `json:"login"`
}

type ghJobsResponse struct {
	Jobs []ghJob `json:"jobs"`
}

type ghJob struct {
	ID    int64    `json:"id"`
	Name  string   `json:"name"`
	Steps []ghStep `json:"steps"`
}

type ghStep struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	Number     int       `json:"number"`
	StartedAt  time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// --- HTTP helpers ------------------------------------------------------------

func (a *GHActionsAdapter) listRuns(ctx context.Context, repo string, since time.Time) ([]ghRun, error) {
	url := fmt.Sprintf("%s/repos/%s/actions/runs?per_page=100&created=>%s",
		a.apiBase, repo, since.UTC().Format(time.RFC3339))

	var result ghRunsResponse
	if err := a.get(ctx, url, &result); err != nil {
		return nil, err
	}
	return result.WorkflowRuns, nil
}

func (a *GHActionsAdapter) listJobs(ctx context.Context, repo string, runID int64) ([]ghJob, error) {
	url := fmt.Sprintf("%s/repos/%s/actions/runs/%d/jobs?per_page=100",
		a.apiBase, repo, runID)

	var result ghJobsResponse
	if err := a.get(ctx, url, &result); err != nil {
		return nil, err
	}
	return result.Jobs, nil
}

func (a *GHActionsAdapter) get(ctx context.Context, url string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("http get %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github api %s: status %d: %s", url, resp.StatusCode, body)
	}

	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("unmarshal response: %w", err)
	}
	return nil
}
