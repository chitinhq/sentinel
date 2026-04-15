package insights

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubDenyQuerier lets us exercise gatherGovernanceDenies error paths
// without a live pgxpool. Only Query is used by the function under test.
type stubDenyQuerier struct {
	err error
}

func (s stubDenyQuerier) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, s.err
}

// TestGatherInputs_GovernanceDenies verifies that governance deny counts
// flow through detectSignals into CategoryGovernance (a dedicated
// category, separate from CategoryPattern), and that the governance
// prompt is wired correctly. We don't stand up a real pgxpool here —
// instead we populate GeneratorInputs directly.
func TestGatherInputs_GovernanceDenies(t *testing.T) {
	g := &Generator{scoreDelta: 5, volumeSpike: 3.0}

	// Baseline: no deny spike → no governance category.
	inputs := &GeneratorInputs{
		GovernanceDenyCounts: map[string]int{"rate_limited": 3, "dangerous": 2},
	}
	cats := g.detectSignals(inputs)
	for _, c := range cats {
		if c == CategoryGovernance {
			t.Fatalf("expected no governance category with denies <= 10, got %v", cats)
		}
	}

	// Spike: one reason > 10 → governance category fires.
	inputs.GovernanceDenyCounts = map[string]int{"rate_limited": 42, "dangerous": 2}
	cats = g.detectSignals(inputs)
	found := false
	for _, c := range cats {
		if c == CategoryGovernance {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CategoryGovernance to fire on deny spike, got %v", cats)
	}

	up := buildGovernancePrompt(inputs.GovernanceDenyCounts)
	if !strings.Contains(up, "rate_limited") {
		t.Fatalf("governance prompt missing deny reason: %s", up)
	}
	sp := governancePatternSystemPrompt()
	if !strings.Contains(sp, "deny_reason") {
		t.Fatalf("governance system prompt missing deny_reason contract: %s", sp)
	}
}

// TestDetectSignals_GovernanceAndPatternCoFire verifies the design-bug
// fix: when BOTH execution failures and a governance deny spike are
// present (the common case in production), BOTH CategoryPattern and
// CategoryGovernance fire. Previously governance was only selected
// when failure_count == 0, causing the dedicated governance briefing
// to never run in practice.
func TestDetectSignals_GovernanceAndPatternCoFire(t *testing.T) {
	g := &Generator{scoreDelta: 5, volumeSpike: 3.0}
	inputs := &GeneratorInputs{
		FailureCounts:        map[string]int{"chitinhq/kernel": 7},
		GovernanceDenyCounts: map[string]int{"rate_limited": 42},
	}
	cats := g.detectSignals(inputs)

	var sawPattern, sawGov bool
	for _, c := range cats {
		if c == CategoryPattern {
			sawPattern = true
		}
		if c == CategoryGovernance {
			sawGov = true
		}
	}
	if !sawPattern {
		t.Fatalf("expected CategoryPattern with failures > 0, got %v", cats)
	}
	if !sawGov {
		t.Fatalf("expected CategoryGovernance with deny spike, got %v", cats)
	}
}

// TestGatherGovernanceDenies_QueryError verifies that a query failure
// (missing table during migration, DB down, etc.) returns an error
// rather than silently swallowing the signal-layer outage. This is
// the companion to the log-and-continue behavior in gatherInputs.
func TestGatherGovernanceDenies_QueryError(t *testing.T) {
	sentinel := errors.New("relation \"governance_events\" does not exist")
	out, err := gatherGovernanceDenies(context.Background(), stubDenyQuerier{err: sentinel})
	if err == nil {
		t.Fatalf("expected error when Query fails, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty map on query error, got %v", out)
	}

	// Nil querier is a benign no-op (no DB configured yet).
	out, err = gatherGovernanceDenies(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error on nil querier, got %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty map on nil querier, got %v", out)
	}
}

// TestDispatchPromptToCLI_FallbackToAPI verifies that setting
// SENTINEL_INSIGHTS_USE_API=true routes callLLM through the Ollama
// Cloud HTTP path (OpenAI-compatible chat-completions) instead of
// shelling out to octi. We point the generator at a test HTTP server
// and confirm it receives the request at the chat-completions
// endpoint with a Bearer auth header.
func TestDispatchPromptToCLI_FallbackToAPI(t *testing.T) {
	hits := 0
	var sawPath, sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		sawPath = r.URL.Path
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[]"}}]}`))
	}))
	defer srv.Close()

	t.Setenv(dispatchCLIEnvVar, "true")

	g := &Generator{
		apiURL:     srv.URL,
		apiKey:     "test-key",
		model:      "glm-5.1:cloud",
		httpClient: srv.Client(),
	}

	out, err := g.callLLM(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("callLLM with API fallback failed: %v", err)
	}
	if out != "[]" {
		t.Fatalf("expected '[]' from API mock, got %q", out)
	}
	if hits != 1 {
		t.Fatalf("expected 1 HTTP hit to fake Ollama Cloud, got %d", hits)
	}
	if sawPath != "/v1/chat/completions" {
		t.Fatalf("expected POST to /v1/chat/completions, got %q", sawPath)
	}
	if sawAuth != "Bearer test-key" {
		t.Fatalf("expected Bearer auth header, got %q", sawAuth)
	}

	// With the env var unset (or false), callLLM routes to octi CLI,
	// which will fail in this test environment because octi isn't
	// installed. We assert the error surface matches the CLI path so
	// the transport selection is provably env-gated.
	_ = os.Unsetenv(dispatchCLIEnvVar)
	_, err = g.callLLM(context.Background(), "sys", "user")
	if err == nil {
		t.Fatalf("expected octi CLI exec error when env var unset")
	}
	if !strings.Contains(err.Error(), "octi dispatch") {
		t.Fatalf("expected octi dispatch error, got: %v", err)
	}
}
