package analyzer

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
)

// Store is the data-access interface the Analyzer requires.
// QueryEvents returns []Event (analyzer.Event) so that detection passes
// can consume events directly without an extra conversion layer.
// Use a thin adapter (see db.StoreAdapter) to wrap a db.NeonClient.
type Store interface {
	QueryEvents(ctx context.Context, since, until time.Time) ([]Event, error)
	QueryActionCounts(ctx context.Context, since time.Time) ([]db.ActionCount, error)
	QueryDenialRates(ctx context.Context, since time.Time) ([]db.DenialRate, error)
	QuerySessionDenials(ctx context.Context, since time.Time) ([]db.SessionDenialCount, error)
	QueryHourlyVolumes(ctx context.Context, since time.Time) ([]db.HourlyVolume, error)
	Close()
}

// Analyzer orchestrates all five detection passes and returns a unified
// slice of findings sorted by pass order.
type Analyzer struct {
	store Store
	cfg   *config.Config
}

// New constructs an Analyzer backed by the given store and configuration.
func New(store Store, cfg *config.Config) *Analyzer {
	return &Analyzer{store: store, cfg: cfg}
}

// Run executes all five detection passes in sequence and returns every
// finding they produce.  An error from any store query aborts the run.
func (a *Analyzer) Run(ctx context.Context) ([]Finding, error) {
	now := time.Now()
	since := now.Add(-a.cfg.Analysis.Lookback)
	baselineSince := now.Add(-a.cfg.Analysis.TrendWindow)
	var all []Finding

	// Pass 1: Action hotspot ranking
	counts, err := a.store.QueryActionCounts(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("pass 1 (hotspot): %w", err)
	}
	hotspots := DetectHotspots(counts)
	log.Printf("sentinel: pass 1 (hotspot) found %d findings", len(hotspots))
	all = append(all, hotspots...)

	// Pass 2: False positive detection
	currentRates, err := a.store.QueryDenialRates(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("pass 2 current: %w", err)
	}
	baselineRates, err := a.store.QueryDenialRates(ctx, baselineSince)
	if err != nil {
		return nil, fmt.Errorf("pass 2 baseline: %w", err)
	}
	falsePos := DetectFalsePositives(currentRates, baselineRates, a.cfg.Detection.FalsePositive)
	log.Printf("sentinel: pass 2 (false positive) found %d findings", len(falsePos))
	all = append(all, falsePos...)

	// Pass 3: Bypass pattern matching
	events, err := a.store.QueryEvents(ctx, since, now)
	if err != nil {
		return nil, fmt.Errorf("pass 3 (bypass): %w", err)
	}
	bypasses := DetectBypassPatterns(events, a.cfg.Detection.Bypass)
	log.Printf("sentinel: pass 3 (bypass) found %d findings", len(bypasses))
	all = append(all, bypasses...)

	// Pass 4: Tool risk profiling
	toolRisks := ProfileToolRisk(currentRates)
	log.Printf("sentinel: pass 4 (tool risk) found %d findings", len(toolRisks))
	all = append(all, toolRisks...)

	// Pass 5: Anomaly detection
	volumes, err := a.store.QueryHourlyVolumes(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("pass 5 volumes: %w", err)
	}
	sessionDenials, err := a.store.QuerySessionDenials(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("pass 5 sessions: %w", err)
	}
	anomalies := DetectAnomalies(volumes, sessionDenials, a.cfg.Detection.Anomaly)
	log.Printf("sentinel: pass 5 (anomaly) found %d findings", len(anomalies))
	all = append(all, anomalies...)

	// Pass 6: Drift detection - need baseline events
	baselineEvents, err := a.store.QueryEvents(ctx, baselineSince, now)
	if err != nil {
		return nil, fmt.Errorf("pass 6 (drift): %w", err)
	}
	driftFindings := DetectDrift(events, baselineEvents, a.cfg.Detection.Drift)
	log.Printf("sentinel: pass 6 (drift) found %d findings", len(driftFindings))
	all = append(all, driftFindings...)

	log.Printf("sentinel: total findings: %d", len(all))
	return all, nil
}
