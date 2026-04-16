package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chitinhq/sentinel/internal/circuit"
	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
)

// runPatrol launches the four-signal circuit patrol as a long-running
// goroutine. Blocks on SIGINT/SIGTERM. Architecture: orthogonal patrol,
// never inline — the patrol writes circuit.<signal> events onto the
// shared events.jsonl stream and Octi's dispatcher subscribes there.
func runPatrol() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(configPath())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.Circuit.Enabled {
		log.Println("sentinel patrol: circuit.enabled=false in sentinel.yaml — exiting")
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

	repos := cfg.Circuit.Repos
	if len(repos) == 0 {
		repos = cfg.Ingestion.GitHubActions.Repos
	}

	thresholds := thresholdsFromConfig(cfg.Circuit)
	source := circuit.NewNeonSignalSource(neon.Pool(), repos)
	emitter := circuit.FlowEmitter{}
	breaker := circuit.New(thresholds, source, emitter)

	interval := cfg.Circuit.PatrolInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}

	logger := log.New(os.Stderr, "circuit ", log.LstdFlags)
	logger.Printf("starting patrol: interval=%s repos=%v", interval, repos)

	patrol := circuit.NewPatrol(breaker, interval, logger)
	err = patrol.Run(ctx)
	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		return err
	}
	logger.Println("patrol exited cleanly")
	return nil
}

// thresholdsFromConfig maps the YAML CircuitConfig onto circuit.Thresholds.
// Zero-valued fields fall through to circuit.DefaultThresholds() so a
// partially-specified sentinel.yaml never silently disables a signal.
func thresholdsFromConfig(c config.CircuitConfig) circuit.Thresholds {
	d := circuit.DefaultThresholds()
	t := circuit.Thresholds{
		MaxRetriesPerTask:        firstNonZeroInt(c.MaxRetriesPerTask, d.MaxRetriesPerTask),
		RetryWindow:              firstNonZeroDur(c.RetryWindow, d.RetryWindow),
		MaxActiveAgents:          firstNonZeroInt(c.MaxActiveAgents, d.MaxActiveAgents),
		MaxSessionDuration:       firstNonZeroDur(c.MaxSessionDuration, d.MaxSessionDuration),
		MaxTokenBurnRate:         firstNonZeroFloat(c.MaxTokenBurnRate, d.MaxTokenBurnRate),
		MaxOpenPRsPerRepo:        firstNonZeroInt(c.MaxOpenPRsPerRepo, d.MaxOpenPRsPerRepo),
		MaxCIFailureRate:         firstNonZeroFloat(c.MaxCIFailureRate, d.MaxCIFailureRate),
		CIWindow:                 firstNonZeroDur(c.CIWindow, d.CIWindow),
		MinRequiredFieldCoverage: firstNonZeroFloat(c.MinRequiredFieldCoverage, d.MinRequiredFieldCoverage),
		TelemetryWindow:          firstNonZeroDur(c.TelemetryWindow, d.TelemetryWindow),
	}
	return t
}

func firstNonZeroInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}
func firstNonZeroDur(a, b time.Duration) time.Duration {
	if a != 0 {
		return a
	}
	return b
}
func firstNonZeroFloat(a, b float64) float64 {
	if a != 0 {
		return a
	}
	return b
}
