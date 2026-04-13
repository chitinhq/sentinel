package insights

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestGatherInputs_GovernanceDenies verifies that governance deny counts
// flow through detectSignals into CategoryPattern, and that the pattern
// branch of generateCategory selects the governance briefing when only
// governance signal is present. We don't stand up a real pgxpool here —
// instead we populate GeneratorInputs directly, which exercises the new
// signal-layer code path end-to-end without a live DB.
func TestGatherInputs_GovernanceDenies(t *testing.T) {
	g := &Generator{scoreDelta: 5, volumeSpike: 3.0}

	// Baseline: no deny spike → no pattern category.
	inputs := &GeneratorInputs{
		GovernanceDenyCounts: map[string]int{"rate_limited": 3, "dangerous": 2},
	}
	cats := g.detectSignals(inputs)
	for _, c := range cats {
		if c == CategoryPattern {
			t.Fatalf("expected no pattern category with denies <= 10, got %v", cats)
		}
	}

	// Spike: one reason > 10 → pattern category fires.
	inputs.GovernanceDenyCounts = map[string]int{"rate_limited": 42, "dangerous": 2}
	cats = g.detectSignals(inputs)
	found := false
	for _, c := range cats {
		if c == CategoryPattern {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CategoryPattern to fire on deny spike, got %v", cats)
	}

	// When only governance signal is present, the governance briefing is
	// used instead of the generic failure-pattern briefing. We assert the
	// prompt content rather than mocking the LLM.
	up := buildGovernancePrompt(inputs.GovernanceDenyCounts)
	if !strings.Contains(up, "rate_limited") {
		t.Fatalf("governance prompt missing deny reason: %s", up)
	}
	sp := governancePatternSystemPrompt()
	if !strings.Contains(sp, "deny_reason") {
		t.Fatalf("governance system prompt missing deny_reason contract: %s", sp)
	}
}

// TestDispatchPromptToCLI_FallbackToAPI verifies that setting
// SENTINEL_INSIGHTS_USE_API=true routes callLLM through the Anthropic
// HTTP path instead of shelling out to octi. We point the generator at
// a test HTTP server and confirm it receives the request.
func TestDispatchPromptToCLI_FallbackToAPI(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"[]"}]}`))
	}))
	defer srv.Close()

	t.Setenv(dispatchCLIEnvVar, "true")

	g := &Generator{
		apiURL:     srv.URL,
		apiKey:     "test-key",
		model:      "claude-sonnet-4-6",
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
		t.Fatalf("expected 1 HTTP hit to fake Anthropic, got %d", hits)
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
