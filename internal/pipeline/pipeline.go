// Package pipeline wires the three Sentinel analysis stages:
//
//	Analyzer → Interpreter → Router
//
// It is the single entry point for running a complete analysis cycle.
package pipeline

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/chitinhq/sentinel/internal/analyzer"
	"github.com/chitinhq/sentinel/internal/config"
	"github.com/chitinhq/sentinel/internal/db"
	"github.com/chitinhq/sentinel/internal/flow"
	"github.com/chitinhq/sentinel/internal/memory"
	"github.com/chitinhq/sentinel/internal/router"
)

// Interpreter is the interface the pipeline requires from Stage 2.
// The concrete implementation is *interpreter.Interpreter; the interface
// lets tests inject a stub without a live Anthropic API key.
type Interpreter interface {
	Interpret(ctx context.Context, findings []analyzer.Finding) ([]analyzer.InterpretedFinding, error)
}

// RunResult summarises a single pipeline execution.
type RunResult struct {
	TotalFindings    int
	HighConfidence   int
	MediumConfidence int
	LowConfidence    int
	Duplicates       int
	Interpreted      []analyzer.InterpretedFinding
	Decisions        []analyzer.RoutingDecision
	IssueURLs        map[string]string
}

// Pipeline holds the wired-up dependencies for a full analysis run.
type Pipeline struct {
	cfg    *config.Config
	store  analyzer.Store
	interp Interpreter
	mem    memory.MemoryClient
	gh     router.GitHubClient
}

// New constructs a Pipeline.  All parameters are required (pass mocks in tests).
func New(
	cfg *config.Config,
	store analyzer.Store,
	interp Interpreter,
	mem memory.MemoryClient,
	gh router.GitHubClient,
) *Pipeline {
	return &Pipeline{
		cfg:    cfg,
		store:  store,
		interp: interp,
		mem:    mem,
		gh:     gh,
	}
}

// Analyze runs all three stages and returns a consolidated RunResult.
//
// Stage 1 — Analyzer: runs 5 detection passes against the telemetry store.
// Stage 2 — Interpreter: enriches each finding with LLM confidence scores.
// Stage 3 — Router: gates findings by confidence and dispatches to sinks.
func (p *Pipeline) Analyze(ctx context.Context) (*RunResult, error) {
	// --- Stage 1: Analyzer --------------------------------------------------
	a := analyzer.New(p.store, p.cfg)
	findings, err := a.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("analyzer: %w", err)
	}
	log.Printf("pipeline: stage 1 complete — %d findings", len(findings))

	// --- Stage 2: Interpreter -----------------------------------------------
	var interpreted []analyzer.InterpretedFinding
	if err := flow.Span("sentinel.interpret", nil, func() error {
		var ierr error
		interpreted, ierr = p.interp.Interpret(ctx, findings)
		if ierr != nil {
			return ierr
		}
		flow.Complete("sentinel.interpret.count", map[string]any{"interpreted": len(interpreted)})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("interpreter: %w", err)
	}
	log.Printf("pipeline: stage 2 complete — %d interpreted findings", len(interpreted))

	// --- Stage 3: Router ----------------------------------------------------
	r := router.New(p.cfg.Routing, p.mem, p.gh, p.cfg.GitHub)
	var decisions []analyzer.RoutingDecision
	_ = flow.Span("sentinel.route", nil, func() error {
		var rerr error
		decisions, rerr = r.RouteAll(ctx, interpreted)
		if rerr != nil {
			// RouteAll collects per-finding errors but still returns decisions;
			// log and continue so we don't lose partial results.
			log.Printf("pipeline: router errors (non-fatal): %v", rerr)
		}
		flow.Complete("sentinel.route.count", map[string]any{"decisions": len(decisions)})
		return nil
	})
	log.Printf("pipeline: stage 3 complete — %d routing decisions", len(decisions))

	// --- Collect GitHub issue URLs ------------------------------------------
	// The router sets GitHubIssue=true for high-confidence actionable findings
	// that pass dedup; the pipeline then executes the actual CreateIssue call.
	issueURLs := make(map[string]string)
	for i, d := range decisions {
		if !d.GitHubIssue || i >= len(interpreted) {
			continue
		}
		url, issueErr := p.gh.CreateIssue(ctx, interpreted[i], p.cfg.GitHub.Repo, p.cfg.GitHub.Labels)
		if issueErr != nil {
			log.Printf("pipeline: create issue for %s: %v", interpreted[i].Finding.ID, issueErr)
			continue
		}
		issueURLs[interpreted[i].Finding.ID] = url
	}

	// --- Build result -------------------------------------------------------
	result := &RunResult{
		TotalFindings: len(interpreted),
		Interpreted:   interpreted,
		Decisions:     decisions,
		IssueURLs:     issueURLs,
	}

	for _, f := range interpreted {
		switch {
		case f.Confidence >= p.cfg.Routing.HighConfidence:
			result.HighConfidence++
		case f.Confidence >= p.cfg.Routing.MediumConfidence:
			result.MediumConfidence++
		default:
			result.LowConfidence++
		}
	}
	for _, d := range decisions {
		if d.IsDuplicate {
			result.Duplicates++
		}
	}

	return result, nil
}

// StoreAdapter wraps a db.EventStore and implements analyzer.Store by
// converting db.Event slices to analyzer.Event slices.  Other return types
// (db.ActionCount, db.DenialRate, etc.) are shared between packages and
// pass through without conversion.
//
// The adapter lives in the pipeline package (not db) to avoid the import cycle:
// analyzer → db → analyzer.
type StoreAdapter struct {
	inner db.EventStore
}

// NewStoreAdapter wraps any db.EventStore so it satisfies analyzer.Store.
func NewStoreAdapter(inner db.EventStore) *StoreAdapter {
	return &StoreAdapter{inner: inner}
}

// QueryEvents converts db.Event rows to analyzer.Event values.
func (a *StoreAdapter) QueryEvents(ctx context.Context, since, until time.Time) ([]analyzer.Event, error) {
	dbEvents, err := a.inner.QueryEvents(ctx, since, until)
	if err != nil {
		return nil, err
	}
	out := make([]analyzer.Event, len(dbEvents))
	for i, e := range dbEvents {
		out[i] = analyzer.Event{
			ID:            e.ID,
			Timestamp:     e.Timestamp,
			AgentID:       e.AgentID,
			SessionID:     e.SessionID,
			EventType:     e.EventType,
			Action:        e.Action,
			Resource:      e.Resource,
			Outcome:       e.Outcome,
			RiskLevel:     e.RiskLevel,
			PolicyVersion: e.PolicyVersion,
			Metadata:      e.Metadata,
		}
	}
	return out, nil
}

// QueryActionCounts delegates directly to the inner store.
func (a *StoreAdapter) QueryActionCounts(ctx context.Context, since time.Time) ([]db.ActionCount, error) {
	return a.inner.QueryActionCounts(ctx, since)
}

// QueryDenialRates delegates directly to the inner store.
func (a *StoreAdapter) QueryDenialRates(ctx context.Context, since time.Time) ([]db.DenialRate, error) {
	return a.inner.QueryDenialRates(ctx, since)
}

// QuerySessionDenials delegates directly to the inner store.
func (a *StoreAdapter) QuerySessionDenials(ctx context.Context, since time.Time) ([]db.SessionDenialCount, error) {
	return a.inner.QuerySessionDenials(ctx, since)
}

// QueryHourlyVolumes delegates directly to the inner store.
func (a *StoreAdapter) QueryHourlyVolumes(ctx context.Context, since time.Time) ([]db.HourlyVolume, error) {
	return a.inner.QueryHourlyVolumes(ctx, since)
}

// QueryCommandFailureRates delegates directly to the inner store.
func (a *StoreAdapter) QueryCommandFailureRates(ctx context.Context, since time.Time) ([]db.CommandFailureRate, error) {
	return a.inner.QueryCommandFailureRates(ctx, since)
}

// QuerySessionSequences delegates directly to the inner store.
func (a *StoreAdapter) QuerySessionSequences(ctx context.Context, since time.Time) ([]db.SessionSequence, error) {
	return a.inner.QuerySessionSequences(ctx, since)
}

// Close delegates to the inner store.
func (a *StoreAdapter) Close() {
	a.inner.Close()
}
