package analyzer

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
	"github.com/chitinhq/sentinel/internal/flow"
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
	QueryCommandFailureRates(ctx context.Context, since time.Time) ([]db.CommandFailureRate, error)
	QuerySessionSequences(ctx context.Context, since time.Time) ([]db.SessionSequence, error)
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
	var all []Finding
	runErr := flow.Span("sentinel.analyze", nil, func() error {
		now := time.Now()
		since := now.Add(-a.cfg.Analysis.Lookback)
		baselineSince := now.Add(-a.cfg.Analysis.TrendWindow)

		// Pass 1: Action hotspot ranking
		var counts []db.ActionCount
		if err := flow.Span("sentinel.analyze.pass.hotspot", nil, func() error {
			var qerr error
			counts, qerr = a.store.QueryActionCounts(ctx, since)
			if qerr != nil {
				return qerr
			}
			hotspots := DetectHotspots(counts)
			log.Printf("sentinel: pass 1 (hotspot) found %d findings", len(hotspots))
			all = append(all, hotspots...)
			flow.Complete("sentinel.analyze.pass.hotspot.findings", map[string]any{"findings": len(hotspots)})
			return nil
		}); err != nil {
			return fmt.Errorf("pass 1 (hotspot): %w", err)
		}

		// Pass 2: False positive detection
		var currentRates []db.DenialRate
		if err := flow.Span("sentinel.analyze.pass.falsepos", nil, func() error {
			var qerr error
			currentRates, qerr = a.store.QueryDenialRates(ctx, since)
			if qerr != nil {
				return fmt.Errorf("current: %w", qerr)
			}
			baselineRates, qerr := a.store.QueryDenialRates(ctx, baselineSince)
			if qerr != nil {
				return fmt.Errorf("baseline: %w", qerr)
			}
			falsePos := DetectFalsePositives(currentRates, baselineRates, a.cfg.Detection.FalsePositive)
			log.Printf("sentinel: pass 2 (false positive) found %d findings", len(falsePos))
			all = append(all, falsePos...)
			flow.Complete("sentinel.analyze.pass.falsepos.findings", map[string]any{"findings": len(falsePos)})
			return nil
		}); err != nil {
			return fmt.Errorf("pass 2: %w", err)
		}

		// Pass 3: Bypass pattern matching
		if err := flow.Span("sentinel.analyze.pass.bypass", nil, func() error {
			events, qerr := a.store.QueryEvents(ctx, since, now)
			if qerr != nil {
				return qerr
			}
			bypasses := DetectBypassPatterns(events, a.cfg.Detection.Bypass)
			log.Printf("sentinel: pass 3 (bypass) found %d findings", len(bypasses))
			all = append(all, bypasses...)
			flow.Complete("sentinel.analyze.pass.bypass.findings", map[string]any{"findings": len(bypasses)})
			return nil
		}); err != nil {
			return fmt.Errorf("pass 3 (bypass): %w", err)
		}

		// Pass 4: Tool risk profiling
		_ = flow.Span("sentinel.analyze.pass.toolrisk", nil, func() error {
			toolRisks := ProfileToolRisk(currentRates)
			log.Printf("sentinel: pass 4 (tool risk) found %d findings", len(toolRisks))
			all = append(all, toolRisks...)
			flow.Complete("sentinel.analyze.pass.toolrisk.findings", map[string]any{"findings": len(toolRisks)})
			return nil
		})

		// Pass 4b: MCP usage profiling
		_ = flow.Span("sentinel.analyze.pass.mcpusage", nil, func() error {
			mcpUsage := ProfileMCPUsage(counts)
			log.Printf("sentinel: pass 4b (mcp usage) found %d findings", len(mcpUsage))
			all = append(all, mcpUsage...)
			flow.Complete("sentinel.analyze.pass.mcpusage.findings", map[string]any{"findings": len(mcpUsage)})
			return nil
		})

		// Pass 5: Anomaly detection
		if err := flow.Span("sentinel.analyze.pass.anomaly", nil, func() error {
			volumes, qerr := a.store.QueryHourlyVolumes(ctx, since)
			if qerr != nil {
				return fmt.Errorf("volumes: %w", qerr)
			}
			sessionDenials, qerr := a.store.QuerySessionDenials(ctx, since)
			if qerr != nil {
				return fmt.Errorf("sessions: %w", qerr)
			}
			anomalies := DetectAnomalies(volumes, sessionDenials, a.cfg.Detection.Anomaly)
			log.Printf("sentinel: pass 5 (anomaly) found %d findings", len(anomalies))
			all = append(all, anomalies...)
			flow.Complete("sentinel.analyze.pass.anomaly.findings", map[string]any{"findings": len(anomalies)})
			return nil
		}); err != nil {
			return fmt.Errorf("pass 5: %w", err)
		}

		// Pass 6 + 7: only if ingestion enabled
		if a.cfg.Ingestion.Enabled {
			_ = flow.Span("sentinel.analyze.pass.cmdfail", nil, func() error {
				cmdRates, qerr := a.store.QueryCommandFailureRates(ctx, since)
				if qerr != nil {
					log.Printf("sentinel: pass 6 (command failure): %v", qerr)
					return qerr
				}
				cmdFailures := DetectCommandFailures(cmdRates, a.cfg.ExecutionPasses.CommandFailure)
				log.Printf("sentinel: pass 6 (command failure) found %d findings", len(cmdFailures))
				all = append(all, cmdFailures...)
				flow.Complete("sentinel.analyze.pass.cmdfail.findings", map[string]any{"findings": len(cmdFailures)})
				return nil
			})

			_ = flow.Span("sentinel.analyze.pass.sequence", nil, func() error {
				sequences, qerr := a.store.QuerySessionSequences(ctx, since)
				if qerr != nil {
					log.Printf("sentinel: pass 7 (sequence): %v", qerr)
					return qerr
				}
				seqFailures := DetectFailureSequences(sequences, a.cfg.ExecutionPasses.SequenceDetection)
				log.Printf("sentinel: pass 7 (sequence) found %d findings", len(seqFailures))
				all = append(all, seqFailures...)
				flow.Complete("sentinel.analyze.pass.sequence.findings", map[string]any{"findings": len(seqFailures)})
				return nil
			})
		}

		log.Printf("sentinel: total findings: %d", len(all))
		return nil
	})
	if runErr != nil {
		return nil, runErr
	}
	return all, nil
}
