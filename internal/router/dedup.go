package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
)

// GitHubClient abstracts GitHub issue operations so the router can work
// against a real gh-CLI backend or a test mock.
type GitHubClient interface {
	SearchIssues(ctx context.Context, query string) ([]string, error)
	CreateIssue(ctx context.Context, finding analyzer.InterpretedFinding, repo string, labels []string) (string, error)
}

// checkDuplicate returns true when an equivalent open issue already exists,
// either in GitHub or in Qdrant memory.  It checks both sources and returns
// on the first positive match.
func checkDuplicate(
	ctx context.Context,
	finding analyzer.InterpretedFinding,
	gh GitHubClient,
	mem memory.MemoryClient,
) (bool, error) {
	// --- GitHub search --------------------------------------------------
	// Build a narrow title query so we get precise matches.
	query := fmt.Sprintf("%s %s", finding.Finding.PolicyID, finding.Finding.Pass)
	query = strings.TrimSpace(query)

	issues, err := gh.SearchIssues(ctx, query)
	if err != nil {
		// Don't fail the whole pipeline on a search error; log and continue.
		// The caller receives (false, err) and can decide.
		return false, fmt.Errorf("github search: %w", err)
	}
	if len(issues) > 0 {
		return true, nil
	}

	// --- Qdrant memory recall -------------------------------------------
	// Phase 1: Recall is a no-op (returns nil, nil) per memory/client.go.
	// We still call it so the dedup path is wired correctly for Phase 2.
	memQuery := fmt.Sprintf("policy:%s pass:%s", finding.Finding.PolicyID, finding.Finding.Pass)
	entries, err := mem.Recall(ctx, memQuery, 5)
	if err != nil {
		return false, fmt.Errorf("memory recall: %w", err)
	}
	if len(entries) > 0 {
		return true, nil
	}

	return false, nil
}
