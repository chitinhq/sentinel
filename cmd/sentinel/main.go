package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
	"github.com/chitinhq/sentinel/internal/flow"
	"github.com/chitinhq/sentinel/internal/health"
	"github.com/chitinhq/sentinel/internal/heartbeat"
	"github.com/chitinhq/sentinel/internal/ingestion"
	"github.com/chitinhq/sentinel/internal/insights"
	"github.com/chitinhq/sentinel/internal/interpreter"
	"github.com/chitinhq/sentinel/internal/memory"
	"github.com/chitinhq/sentinel/internal/pipeline"
	"github.com/chitinhq/sentinel/internal/router"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sentinel <analyze|digest|ingest|health|heartbeat|flows>")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "flows":
		if err := runFlows(); err != nil {
			fmt.Fprintf(os.Stderr, "flows failed: %v\n", err)
			os.Exit(1)
		}
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
	case "ingest":
		if err := runIngest(); err != nil {
			fmt.Fprintf(os.Stderr, "ingest failed: %v\n", err)
			os.Exit(1)
		}
	case "health":
		if err := runHealth(); err != nil {
			fmt.Fprintf(os.Stderr, "health failed: %v\n", err)
			os.Exit(1)
		}
	case "heartbeat":
		code, err := runHeartbeat()
		if err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(code)
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

	// --- Insight generation (post-analysis) ---
	if cfg.Insights.Enabled && cfg.AnthropicAPIKey != "" {
		redisClient, redisErr := connectRedis(cfg)
		if redisErr != nil {
			log.Printf("sentinel: redis unavailable for insights: %v", redisErr)
		}

		neonForInsights, neonErr := db.NewNeonClient(ctx, cfg.NeonDatabaseURL)
		if neonErr != nil {
			log.Printf("sentinel: neon connect for insights failed: %v", neonErr)
			return nil
		}
		defer neonForInsights.Close()

		gen := insights.NewGenerator(
			neonForInsights.Pool(),
			redisClient,
			insights.GeneratorConfig{
				APIKey:               cfg.AnthropicAPIKey,
				Model:                cfg.Interpreter.Model,
				MaxFrequencyMinutes:  cfg.Insights.MaxFrequencyMinutes,
				ScoreDeltaThreshold:  cfg.Insights.ScoreDeltaThreshold,
				VolumeSpikeThreshold: cfg.Insights.VolumeSpikeThreshold,
				NtfyTopic:            getEnvDefault("NTFY_TOPIC", "chitin"),
			},
		)

		generated, err := gen.MaybeGenerate(ctx)
		if err != nil {
			log.Printf("sentinel: insight generation failed: %v", err)
		} else if len(generated) > 0 {
			log.Printf("sentinel: generated %d insights", len(generated))
			for _, ins := range generated {
				log.Printf("  [%s/%s] %s", ins.Category, ins.Severity, truncateStr(ins.Narrative, 100))
			}
		} else {
			log.Println("sentinel: no signal for insight generation")
		}

		if redisClient != nil {
			redisClient.Close()
		}
	}

	return nil
}

func getEnvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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

// connectRedis creates a Redis client from config.
func connectRedis(cfg *config.Config) (*redis.Client, error) {
	opt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return redis.NewClient(opt), nil
}

// runIngest loads config, connects to Neon, fetches execution events from
// all configured adapters, persists them, and computes health scores.
func runIngest() error {
	return flow.Span("sentinel.ingest", nil, runIngestInner)
}

func runIngestInner() error {
	ctx := context.Background()
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Ingestion.Enabled {
		log.Println("sentinel: ingestion disabled in config")
		return nil
	}
	if cfg.NeonDatabaseURL == "" {
		return fmt.Errorf("NEON_DATABASE_URL is required")
	}
	neon, err := db.NewNeonClient(ctx, cfg.NeonDatabaseURL)
	if err != nil {
		return fmt.Errorf("connect neon: %w", err)
	}
	defer neon.Close()

	store := ingestion.NewStore(neon)
	totalIngested := 0

	// Filter adapter if --adapter flag is set.
	adapterFilter := ""
	for i, arg := range os.Args {
		if arg == "--adapter" && i+1 < len(os.Args) {
			adapterFilter = os.Args[i+1]
		}
	}

	// --- GitHub Actions adapter ---
	if adapterFilter == "" || adapterFilter == "github_actions" {
		var writeErr error
		_ = flow.Span("sentinel.ingest.github_actions", nil, func() error {
			ghAdapter := ingestion.NewGHActionsAdapter(
				cfg.Ingestion.GitHubActions,
				"https://api.github.com",
				cfg.GitHubToken,
			)
			cp, cperr := store.GetCheckpoint(ctx, "github_actions")
			if cperr != nil {
				writeErr = fmt.Errorf("get checkpoint: %w", cperr)
				return cperr
			}
			events, ierr := ghAdapter.Ingest(ctx, cp)
			if ierr != nil {
				log.Printf("sentinel: github_actions ingest error: %v", ierr)
				return ierr
			}
			n, werr := store.Write(ctx, events)
			if werr != nil {
				writeErr = fmt.Errorf("write github_actions events: %w", werr)
				return werr
			}
			if len(events) > 0 {
				last := events[len(events)-1]
				_ = store.SaveCheckpoint(ctx, ingestion.Checkpoint{
					Adapter:   "github_actions",
					LastRunID: last.SessionID,
					LastRunAt: last.Timestamp,
				})
			}
			totalIngested += n
			log.Printf("sentinel: ingested %d events from github_actions", n)
			flow.Complete("sentinel.ingest.github_actions.count", map[string]any{"events": n})
			return nil
		})
		if writeErr != nil {
			return writeErr
		}
	}

	// --- Chitin governance adapter ---
	// Writes directly to governance_events (same table as sentinel-mcp's
	// IngestFile). Previously this path wrote execution_events, which the
	// analyzer never read. See sentinel#31.
	if adapterFilter == "" || adapterFilter == "chitin" {
		if len(cfg.Ingestion.ChitinGovernance.Workspaces) > 0 {
			_ = flow.Span("sentinel.ingest.chitin_governance", nil, func() error {
				writer := &ingestion.PgxGovernanceWriter{Pool: neon.Pool()}
				cgAdapter := ingestion.NewChitinGovernanceAdapter(
					cfg.Ingestion.ChitinGovernance.Workspaces,
					cfg.Tenant.ID,
					writer,
				)
				cp, _ := store.GetCheckpoint(ctx, "chitin_governance")
				n, newCp, err := cgAdapter.Ingest(ctx, cp)
				if err != nil {
					log.Printf("sentinel: chitin_governance ingest error: %v", err)
					return err
				}
				if newCp != nil {
					_ = store.SaveCheckpoint(ctx, *newCp)
				}
				totalIngested += n
				log.Printf("sentinel: ingested %d events from chitin_governance (tenant=%s)", n, cfg.Tenant.ID)
				flow.Complete("sentinel.ingest.chitin_governance.count", map[string]any{"events": n})
				return nil
			})
		}
	}

	// --- Chitin runtime adapter (session/gate/soul events) ---
	if adapterFilter == "" || adapterFilter == "chitin-runtime" {
		rt := cfg.Ingestion.ChitinRuntime
		if rt.StateDir != "" {
			crAdapter := ingestion.NewChitinRuntimeAdapter(rt.StateDir, rt.ShareDir)
			cp, _ := store.GetCheckpoint(ctx, "chitin_runtime")
			events, newCp, err := crAdapter.Ingest(ctx, cp)
			if err != nil {
				log.Printf("sentinel: chitin_runtime ingest error: %v", err)
			} else {
				n, err := store.Write(ctx, events)
				if err != nil {
					return fmt.Errorf("write chitin_runtime events: %w", err)
				}
				if newCp != nil {
					_ = store.SaveCheckpoint(ctx, *newCp)
				}
				totalIngested += n
				log.Printf("sentinel: ingested %d events from chitin_runtime", n)
			}
		}
	}

	// --- Swarm dispatch adapter ---
	if adapterFilter == "" || adapterFilter == "swarm" {
		if cfg.Ingestion.SwarmDispatch.TelemetryPath != "" {
			var writeErr error
			_ = flow.Span("sentinel.ingest.swarm_dispatch", nil, func() error {
				sdAdapter := ingestion.NewSwarmDispatchAdapter(cfg.Ingestion.SwarmDispatch.TelemetryPath)
				cp, _ := store.GetCheckpoint(ctx, "swarm_dispatch")
				events, newCp, ierr := sdAdapter.Ingest(ctx, cp)
				if ierr != nil {
					log.Printf("sentinel: swarm_dispatch ingest error: %v", ierr)
					return ierr
				}
				n, werr := store.Write(ctx, events)
				if werr != nil {
					writeErr = fmt.Errorf("write swarm_dispatch events: %w", werr)
					return werr
				}
				if newCp != nil {
					_ = store.SaveCheckpoint(ctx, *newCp)
				}
				totalIngested += n
				log.Printf("sentinel: ingested %d events from swarm_dispatch", n)
				flow.Complete("sentinel.ingest.swarm_dispatch.count", map[string]any{"events": n})
				return nil
			})
			if writeErr != nil {
				return writeErr
			}
		}
	}

	// --- Brain state adapter ---
	if adapterFilter == "" || adapterFilter == "brain" {
		if cfg.Ingestion.BrainState.Enabled {
			var writeErr error
			_ = flow.Span("sentinel.ingest.brain_state", nil, func() error {
				redisClient, rerr := connectRedis(cfg)
				if rerr != nil {
					log.Printf("sentinel: redis connect error (brain_state skipped): %v", rerr)
					return rerr
				}
				defer redisClient.Close()
				bsAdapter := ingestion.NewBrainStateAdapter(redisClient, cfg.Ingestion.BrainState.Interval)
				cp, _ := store.GetCheckpoint(ctx, "brain_state")
				events, newCp, ierr := bsAdapter.Ingest(ctx, cp)
				if ierr != nil {
					log.Printf("sentinel: brain_state ingest error: %v", ierr)
					return ierr
				}
				n, werr := store.Write(ctx, events)
				if werr != nil {
					writeErr = fmt.Errorf("write brain_state events: %w", werr)
					return werr
				}
				if newCp != nil {
					_ = store.SaveCheckpoint(ctx, *newCp)
				}
				totalIngested += n
				log.Printf("sentinel: ingested %d events from brain_state", n)
				flow.Complete("sentinel.ingest.brain_state.count", map[string]any{"events": n})
				return nil
			})
			if writeErr != nil {
				return writeErr
			}
		}
	}

	log.Printf("sentinel: total ingested %d events across all adapters", totalIngested)

	// --- Compute and persist health scores ---
	if adapterFilter == "" {
		redisClient, err := connectRedis(cfg)
		if err != nil {
			log.Printf("sentinel: redis connect error (health scoring skipped): %v", err)
			return nil
		}
		defer redisClient.Close()

		weights := health.Weights{
			SuccessRate:          cfg.Health.Weights.SuccessRate,
			GovernanceCompliance: cfg.Health.Weights.GovernanceCompliance,
			Latency:              cfg.Health.Weights.Latency,
			BudgetHealth:         cfg.Health.Weights.BudgetHealth,
			Stability:            cfg.Health.Weights.Stability,
		}
		if weights.SuccessRate == 0 {
			weights = health.DefaultWeights()
		}

		scorer := health.NewScorer(neon.Pool(), redisClient, weights)
		scores, err := scorer.ComputeAll(ctx)
		if err != nil {
			log.Printf("sentinel: health scoring error: %v", err)
			return nil
		}

		scorer.EnrichBudgetHealth(ctx, scores)

		if err := scorer.PersistToNeon(ctx, scores); err != nil {
			log.Printf("sentinel: persist health scores error: %v", err)
		}
		if err := scorer.PushToRedis(ctx, scores); err != nil {
			log.Printf("sentinel: push health to redis error: %v", err)
		}

		log.Printf("sentinel: computed %d health scores", len(scores))
	}

	return nil
}

// runHealth displays current health scores.
func runHealth() error {
	ctx := context.Background()
	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.NeonDatabaseURL == "" {
		return fmt.Errorf("NEON_DATABASE_URL is required")
	}
	neon, err := db.NewNeonClient(ctx, cfg.NeonDatabaseURL)
	if err != nil {
		return fmt.Errorf("connect neon: %w", err)
	}
	defer neon.Close()

	// Parse flags.
	var scopeType, scopeValue string
	jsonOutput := false
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--platform":
			scopeType = "platform"
			if i+1 < len(os.Args) {
				scopeValue = os.Args[i+1]
				i++
			}
		case "--repo":
			scopeType = "repo"
			if i+1 < len(os.Args) {
				scopeValue = os.Args[i+1]
				i++
			}
		case "--queue":
			scopeType = "queue"
			if i+1 < len(os.Args) {
				scopeValue = os.Args[i+1]
				i++
			}
		case "--json":
			jsonOutput = true
		}
	}

	scores, err := db.QueryLatestHealthScores(ctx, neon.Pool(), scopeType, scopeValue)
	if err != nil {
		return fmt.Errorf("query health scores: %w", err)
	}

	if jsonOutput {
		data, _ := json.Marshal(scores)
		fmt.Println(string(data))
		return nil
	}

	// Table output.
	fmt.Printf("%-12s %-30s %5s  %7s  %10s  %7s  %6s  %9s  %7s\n",
		"Type", "Value", "Score", "Success", "Governance", "Latency", "Budget", "Stability", "Samples")
	for i := 0; i < 110; i++ {
		fmt.Print("-")
	}
	fmt.Println()
	for _, s := range scores {
		fmt.Printf("%-12s %-30s %5d  %7d  %10d  %7d  %6d  %9d  %7d\n",
			s.ScopeType, s.ScopeValue, s.Score,
			s.Dimensions["success_rate"],
			s.Dimensions["governance_compliance"],
			s.Dimensions["latency"],
			s.Dimensions["budget_health"],
			s.Dimensions["stability"],
			s.SampleSize,
		)
	}
	return nil
}

// runHeartbeat counts governance_events in the last 24h and pages via ntfy
// when volume is below the configured floor. Returns the exit code: 0 if the
// heartbeat is green, 2 if paging (so CI / cron can alert on non-zero exit).
func runHeartbeat() (int, error) {
	ctx := context.Background()
	cfg, err := config.Load(configPath())
	if err != nil {
		return 1, fmt.Errorf("load config: %w", err)
	}
	if cfg.NeonDatabaseURL == "" {
		return 1, fmt.Errorf("NEON_DATABASE_URL is required")
	}
	neon, err := db.NewNeonClient(ctx, cfg.NeonDatabaseURL)
	if err != nil {
		return 1, fmt.Errorf("connect neon: %w", err)
	}
	defer neon.Close()

	counter := &heartbeat.PoolCounter{Pool: neon.Pool()}
	notifier := heartbeat.NewNtfyNotifier(cfg.Heartbeat.NtfyTopic)

	dec, err := heartbeat.Run(ctx, counter, notifier, cfg.Heartbeat.MinEvents24h)
	if err != nil {
		// A counter error means we can't tell if the pipeline is alive.
		// Treat that as a hard failure: log and exit nonzero rather than
		// pretending green. Notifier-only failures still surface here but
		// we prefer a loud exit over a silent "OK".
		return 1, fmt.Errorf("heartbeat run: %w", err)
	}
	if dec.Paging {
		log.Printf("sentinel heartbeat PAGE: %d events in last 24h (threshold %d)", dec.Count, dec.Threshold)
		return 2, nil
	}
	log.Printf("sentinel heartbeat OK: %d events in last 24h (threshold %d)", dec.Count, dec.Threshold)
	return 0, nil
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
