package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/analyzer"
	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
	"github.com/AgentGuardHQ/sentinel/internal/interpreter"
	"github.com/AgentGuardHQ/sentinel/internal/memory"
	"github.com/AgentGuardHQ/sentinel/internal/pipeline"
	"github.com/AgentGuardHQ/sentinel/internal/router"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sentinel <analyze|digest>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "analyze":
		if err := runAnalyze(); err != nil {
			fmt.Fprintf(os.Stderr, "analyze failed: %v\n", err)
			os.Exit(1)
		}
	case "digest":
		if err := runDigest(); err != nil {
			fmt.Fprintf(os.Stderr, "digest failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

// configPath returns the path to sentinel.yaml from the SENTINEL_CONFIG
// environment variable, falling back to "sentinel.yaml" in the working dir.
func configPath() string {
	if p := os.Getenv("SENTINEL_CONFIG"); p != "" {
		return p
	}
	return "sentinel.yaml"
}

// buildPipeline constructs the full analysis pipeline from config + environment.
// The returned cleanup function must be called (e.g. via defer) to close
// the database connection.
func buildPipeline(ctx context.Context, cfg *config.Config) (*pipeline.Pipeline, func(), error) {
	// --- Telemetry store (Neon Postgres) ------------------------------------
	if cfg.NeonDatabaseURL == "" {
		return nil, nil, fmt.Errorf("NEON_DATABASE_URL is required")
	}
	neon, err := db.NewNeonClient(ctx, cfg.NeonDatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("connect neon: %w", err)
	}
	store := pipeline.NewStoreAdapter(neon)

	// --- Memory client (Octi Pulpo) -----------------------------------------
	mem := memory.NewClient(cfg.OctiPulpoURL)

	// --- LLM interpreter ----------------------------------------------------
	var interp pipeline.Interpreter
	if cfg.AnthropicAPIKey != "" {
		interp = interpreter.New(
			"https://api.anthropic.com",
			cfg.AnthropicAPIKey,
			mem,
			cfg.Interpreter,
		)
	} else {
		log.Println("sentinel: ANTHROPIC_API_KEY not set — using passthrough interpreter (zero-confidence)")
		interp = &passthroughInterpreter{}
	}

	// --- GitHub client ------------------------------------------------------
	gh := router.NewGhClient(cfg.GitHub.Repo, cfg.GitHub.Labels)

	cleanup := func() { store.Close() }
	p := pipeline.New(cfg, store, interp, mem, gh)
	return p, cleanup, nil
}

// runAnalyze loads config, builds the pipeline, runs analysis, and logs a
// summary of findings and any GitHub issues created.
func runAnalyze() error {
	ctx := context.Background()

	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	p, cleanup, err := buildPipeline(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := p.Analyze(ctx)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	log.Printf("sentinel analyze complete:")
	log.Printf("  total findings   : %d", result.TotalFindings)
	log.Printf("  high confidence  : %d", result.HighConfidence)
	log.Printf("  medium confidence: %d", result.MediumConfidence)
	log.Printf("  low confidence   : %d", result.LowConfidence)
	log.Printf("  duplicates       : %d", result.Duplicates)
	log.Printf("  issues created   : %d", len(result.IssueURLs))
	for id, url := range result.IssueURLs {
		log.Printf("    %s → %s", id, url)
	}
	return nil
}

// runDigest loads config, runs the pipeline, renders the weekly markdown
// digest, and writes it to disk (plus Slack notification if configured).
func runDigest() error {
	ctx := context.Background()

	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	p, cleanup, err := buildPipeline(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := p.Analyze(ctx)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	now := time.Now().UTC()
	rangeEnd := now
	rangeStart := now.Add(-cfg.Analysis.Lookback)

	markdown := router.RenderDigest(
		result.Interpreted,
		result.Decisions,
		result.IssueURLs,
		1, // single run per invocation
		rangeStart,
		rangeEnd,
	)

	digestDir := "."
	if d := os.Getenv("SENTINEL_DIGEST_DIR"); d != "" {
		digestDir = d
	}

	if err := router.WriteDigest(ctx, markdown, digestDir, cfg.SlackWebhookURL); err != nil {
		return fmt.Errorf("write digest: %w", err)
	}

	log.Printf("sentinel digest complete: %d findings written to %s", result.TotalFindings, digestDir)
	return nil
}

// passthroughInterpreter is used when ANTHROPIC_API_KEY is not set.
// It returns zero-confidence InterpretedFindings so the pipeline can run
// and route findings without live LLM enrichment.
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
