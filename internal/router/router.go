package router

import (
	"context"
	"fmt"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
)

// Router gates findings through confidence thresholds, dedup checks, and
// dispatches to the appropriate sinks (Qdrant memory, GitHub issues, digest).
type Router struct {
	cfg   config.RoutingConfig
	mem   memory.MemoryClient
	gh    GitHubClient
	ghCfg config.GitHubConfig
}

// New constructs a Router.  All dependencies are required; pass mock
// implementations in tests.
func New(cfg config.RoutingConfig, mem memory.MemoryClient, gh GitHubClient, ghCfg config.GitHubConfig) *Router {
	return &Router{cfg: cfg, mem: mem, gh: gh, ghCfg: ghCfg}
}

// Route evaluates a single InterpretedFinding and returns the RoutingDecision.
//
// Routing rules (evaluated in order):
//  1. Always store to Qdrant (memory).
//  2. If confidence >= HighConfidence AND Actionable AND not a duplicate → create GitHub issue.
//  3. If confidence >= MediumConfidence (and not already GitHub-routed) → add to weekly digest.
//  4. Below MediumConfidence → Qdrant only.
func (r *Router) Route(ctx context.Context, finding analyzer.InterpretedFinding) (analyzer.RoutingDecision, error) {
	decision := analyzer.RoutingDecision{
		Qdrant: true, // always stored
	}

	// Store finding summary in memory for future dedup recall.
	content := fmt.Sprintf("policy:%s pass:%s confidence:%.2f actionable:%v",
		finding.Finding.PolicyID,
		finding.Finding.Pass,
		finding.Confidence,
		finding.Actionable,
	)
	topics := []string{finding.Finding.PolicyID, finding.Finding.Pass}
	if _, err := r.mem.Store(ctx, content, topics, "sentinel"); err != nil {
		// Non-fatal: log-worthy but don't block routing.
		_ = err
	}

	// High-confidence + actionable → GitHub issue (if not duplicate).
	if finding.Confidence >= r.cfg.HighConfidence && finding.Actionable {
		isDup, err := checkDuplicate(ctx, finding, r.gh, r.mem)
		if err != nil {
			// Dedup failure is non-fatal; treat as non-duplicate to avoid silence.
			isDup = false
		}

		if isDup {
			decision.IsDuplicate = true
			// Still surface in digest so the CTO sees recurring patterns.
			decision.WeeklyDigest = true
		} else {
			decision.GitHubIssue = true
			// Also add to digest for the weekly summary table.
			decision.WeeklyDigest = true
		}

		return decision, nil
	}

	// Medium-confidence → weekly digest.
	if finding.Confidence >= r.cfg.MediumConfidence {
		decision.WeeklyDigest = true
		return decision, nil
	}

	// Below medium → Qdrant only (already set above).
	return decision, nil
}

// RouteAll is a convenience batch wrapper around Route.  It returns one
// RoutingDecision per finding in the same order.  Errors from individual
// Route calls are collected and returned as a combined error.
func (r *Router) RouteAll(ctx context.Context, findings []analyzer.InterpretedFinding) ([]analyzer.RoutingDecision, error) {
	decisions := make([]analyzer.RoutingDecision, len(findings))
	var errs []error

	for i, f := range findings {
		d, err := r.Route(ctx, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("finding %s: %w", f.Finding.ID, err))
		}
		decisions[i] = d
	}

	if len(errs) > 0 {
		return decisions, fmt.Errorf("RouteAll errors: %v", errs)
	}
	return decisions, nil
}
