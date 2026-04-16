// Package circuit implements a four-signal, orthogonal swarm circuit breaker.
//
// The breaker is NOT an inline blocking gate on the SDLC pipeline. It runs
// as a periodic patrol, reads four independent signal sources, and — when
// any one trips its threshold — emits a `circuit.<signal>` flow event
// (e.g. `circuit.retry_storm`) so downstream dispatchers (Octi) can
// pause dispatching until an operator issues a reset.
//
// The four signals (collapsed from quorum 2026-04-16-0030, davinci +
// shannon agreeing that retry/resource/health/telemetry are all threshold-
// triggered freezes reading the same telemetry stream):
//
//  1. Retry storm    — same task_id seen > N times in window → runaway loop.
//  2. Resource burn  — active agents > cap OR session duration > max OR
//                      token burn-rate > threshold.
//  3. Repo health    — open_pr_count OR CI failure rate per repo > threshold.
//                      Per-repo (not fleet-wide) because curie's prior
//                      mining showed fleet=0.23%, clawta=17%.
//  4. Telemetry integ— events written with missing required fields
//                      (agent_id, session_id, decision_trace) → governance
//                      layer going blind.
//
// The breaker exposes four signal-checker methods on a single struct so a
// caller can either run all four as a batch (Check) or wire individual
// signals into bespoke patrols (CheckRetryStorm etc).
package circuit

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Signal names are stable strings appended to "circuit." for the emitted
// flow event name (e.g. SignalRetryStorm → "circuit.retry_storm") and
// also carried as the "signal" field in the event detail.
const (
	SignalRetryStorm        = "retry_storm"
	SignalResourceBurn      = "resource_burn"
	SignalRepoHealth        = "repo_health"
	SignalTelemetryIntegrity = "telemetry_integrity"
)

// Thresholds configures the per-signal trip points. Zero-valued fields
// disable that sub-check (e.g. MaxSessionDuration=0 → don't trip on long
// sessions). Defaults live in DefaultThresholds; production values come
// from sentinel.yaml / env overrides — see config.CircuitConfig.
type Thresholds struct {
	// Retry storm
	MaxRetriesPerTask int           // >N retries of same task_id in window trips
	RetryWindow       time.Duration // rolling window for the retry count

	// Resource burn
	MaxActiveAgents    int           // >N concurrent agents trips
	MaxSessionDuration time.Duration // any session running longer trips
	MaxTokenBurnRate   float64       // tokens/minute; >N trips

	// Repo health (per-repo, not fleet-wide)
	MaxOpenPRsPerRepo int     // >N open PRs on a single repo trips
	MaxCIFailureRate  float64 // 0.0-1.0 rolling failure rate per repo
	CIWindow          time.Duration

	// Telemetry integrity
	MinRequiredFieldCoverage float64 // e.g. 0.95 = 95% of recent events must have agent_id/session_id
	TelemetryWindow          time.Duration
}

// DefaultThresholds returns the values curie recommended from mined
// telemetry in the 2026-04-16-0030 quorum thread. These are starting
// points; production should override via sentinel.yaml.
//
//   - retry: same task_id > 3 times in 1h  (thread signal #1 "insights 6/9")
//   - resource: session > 2h OR agents > 8 (thread signal #2 "3.1hr max")
//   - repo health: open_prs > 15 OR failure_rate > 0.15
//     (clawta 17%, fleet 0.23% — per-repo matters)
//   - telemetry: < 95% field coverage in last 15m
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxRetriesPerTask:        3,
		RetryWindow:              1 * time.Hour,
		MaxActiveAgents:          8,
		MaxSessionDuration:       2 * time.Hour,
		MaxTokenBurnRate:         100000, // tokens/minute
		MaxOpenPRsPerRepo:        15,
		MaxCIFailureRate:         0.15,
		CIWindow:                 1 * time.Hour,
		MinRequiredFieldCoverage: 0.95,
		TelemetryWindow:          15 * time.Minute,
	}
}

// SignalSource abstracts the four inputs so the breaker can be tested
// deterministically without touching Neon / GitHub. Production wires these
// to the sentinel analyzer + GitHub Actions ingester.
type SignalSource interface {
	// RetryCounts returns task_id → retry count within the retry window.
	RetryCounts(ctx context.Context, window time.Duration) (map[string]int, error)

	// ActiveAgents returns currently-running agents and the oldest session's age.
	// A zero oldest duration means "no active sessions".
	ActiveAgents(ctx context.Context) (count int, oldest time.Duration, tokensPerMin float64, err error)

	// RepoHealth returns per-repo (open_prs, ci_failure_rate) over window.
	RepoHealth(ctx context.Context, window time.Duration) (map[string]RepoStats, error)

	// TelemetryCoverage returns the ratio (0.0–1.0) of recent events carrying
	// all required fields (agent_id, session_id, decision_trace) over window.
	// Returns 1.0 if no events (nothing to be blind about).
	TelemetryCoverage(ctx context.Context, window time.Duration) (coverage float64, sampled int, err error)
}

// RepoStats is the per-repo subset of RepoHealth output.
type RepoStats struct {
	OpenPRs       int
	CIFailureRate float64
}

// Emitter is how a tripped breaker signals Octi. The production impl is
// a flow.Emit wrapper; tests pass a recorder.
type Emitter interface {
	Emit(ctx context.Context, signal string, detail map[string]any)
}

// Trip is a single signal trip event.
type Trip struct {
	Signal    string         // one of the Signal* constants
	Threshold string         // human-readable threshold spec, e.g. "retries>3"
	Sample    map[string]any // representative evidence (which repo, which task_id, etc)
	At        time.Time
}

// Breaker is the orthogonal four-signal circuit breaker. It is safe for
// concurrent use — State() and Reset() take the mutex; Check() does too
// while it publishes tripped state.
type Breaker struct {
	thresholds Thresholds
	source     SignalSource
	emitter    Emitter

	mu         sync.Mutex
	open       bool
	lastTrip   *Trip
	resetCount int
}

// New constructs a breaker with the given thresholds, signal source, and
// emitter. Passing a nil emitter disables event emission (useful for
// dry-runs or tests that only care about State()).
func New(t Thresholds, src SignalSource, e Emitter) *Breaker {
	return &Breaker{thresholds: t, source: src, emitter: e}
}

// State reports whether the breaker is currently open (tripped). When
// open, the returned Trip describes which signal caused the trip. The
// returned Trip is a deep copy — callers may mutate it freely.
func (b *Breaker) State() (open bool, trip *Trip) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open, cloneTrip(b.lastTrip)
}

// cloneTrip returns a deep copy of t so internal lastTrip state is never
// shared with callers (Sample is a map and would otherwise alias).
func cloneTrip(t *Trip) *Trip {
	if t == nil {
		return nil
	}
	cp := *t
	if t.Sample != nil {
		cp.Sample = make(map[string]any, len(t.Sample))
		for k, v := range t.Sample {
			cp.Sample[k] = v
		}
	}
	return &cp
}

// Reset closes the breaker and clears the last trip. Idempotent.
func (b *Breaker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.open = false
	b.lastTrip = nil
	b.resetCount++
}

// Check runs all four signal checks in order and returns the first Trip
// (if any) that fires. A non-nil return means the breaker is now open;
// nil means all signals were within threshold. If the breaker is already
// open, Check is a no-op and returns the existing trip.
//
// Ordering is deterministic (retry → resource → health → telemetry) so
// test assertions are stable and the "first-to-trip" wins cleanly.
func (b *Breaker) Check(ctx context.Context) (*Trip, error) {
	b.mu.Lock()
	if b.open {
		t := cloneTrip(b.lastTrip)
		b.mu.Unlock()
		return t, nil
	}
	b.mu.Unlock()

	checks := []func(context.Context) (*Trip, error){
		b.CheckRetryStorm,
		b.CheckResourceBurn,
		b.CheckRepoHealth,
		b.CheckTelemetryIntegrity,
	}
	for _, c := range checks {
		trip, err := c(ctx)
		if err != nil {
			return nil, err
		}
		if trip != nil {
			// Re-acquire the lock and compare-and-set: another goroutine
			// may have raced to trip() between our initial open-check and
			// here. If they won, return their trip and don't double-emit.
			b.mu.Lock()
			if b.open {
				t := cloneTrip(b.lastTrip)
				b.mu.Unlock()
				return t, nil
			}
			b.open = true
			b.lastTrip = trip
			b.mu.Unlock()
			if b.emitter != nil {
				b.emitter.Emit(ctx, "circuit."+trip.Signal, map[string]any{
					"signal":    trip.Signal,
					"threshold": trip.Threshold,
					"sample":    trip.Sample,
					"at":        trip.At.Format(time.RFC3339Nano),
				})
			}
			return cloneTrip(trip), nil
		}
	}
	return nil, nil
}

// CheckRetryStorm fires when any task_id exceeds MaxRetriesPerTask within
// RetryWindow. Threshold 0 disables.
func (b *Breaker) CheckRetryStorm(ctx context.Context) (*Trip, error) {
	if b.thresholds.MaxRetriesPerTask <= 0 {
		return nil, nil
	}
	counts, err := b.source.RetryCounts(ctx, b.thresholds.RetryWindow)
	if err != nil {
		return nil, fmt.Errorf("retry counts: %w", err)
	}
	// Pick the worst offender deterministically (highest count, then
	// lexicographically smallest task_id as tiebreaker). Map iteration
	// order is randomized in Go, so this avoids flapping samples across
	// runs and makes investigations reproducible.
	var (
		worstTask  string
		worstCount int
		found      bool
	)
	for task, n := range counts {
		if n <= b.thresholds.MaxRetriesPerTask {
			continue
		}
		if !found || n > worstCount || (n == worstCount && task < worstTask) {
			worstTask = task
			worstCount = n
			found = true
		}
	}
	if found {
		return &Trip{
			Signal:    SignalRetryStorm,
			Threshold: fmt.Sprintf("retries>%d within %s", b.thresholds.MaxRetriesPerTask, b.thresholds.RetryWindow),
			Sample:    map[string]any{"task_id": worstTask, "retry_count": worstCount},
			At:        time.Now().UTC(),
		}, nil
	}
	return nil, nil
}

// CheckResourceBurn fires on any of: too many active agents, a session
// running past MaxSessionDuration, or a token-burn spike.
func (b *Breaker) CheckResourceBurn(ctx context.Context) (*Trip, error) {
	t := b.thresholds
	if t.MaxActiveAgents <= 0 && t.MaxSessionDuration <= 0 && t.MaxTokenBurnRate <= 0 {
		return nil, nil
	}
	count, oldest, tpm, err := b.source.ActiveAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("active agents: %w", err)
	}
	if t.MaxActiveAgents > 0 && count > t.MaxActiveAgents {
		return &Trip{
			Signal:    SignalResourceBurn,
			Threshold: fmt.Sprintf("active_agents>%d", t.MaxActiveAgents),
			Sample:    map[string]any{"active_agents": count},
			At:        time.Now().UTC(),
		}, nil
	}
	if t.MaxSessionDuration > 0 && oldest > t.MaxSessionDuration {
		return &Trip{
			Signal:    SignalResourceBurn,
			Threshold: fmt.Sprintf("session_duration>%s", t.MaxSessionDuration),
			Sample:    map[string]any{"oldest_session_seconds": oldest.Seconds()},
			At:        time.Now().UTC(),
		}, nil
	}
	if t.MaxTokenBurnRate > 0 && tpm > t.MaxTokenBurnRate {
		return &Trip{
			Signal:    SignalResourceBurn,
			Threshold: fmt.Sprintf("tokens_per_min>%.0f", t.MaxTokenBurnRate),
			Sample:    map[string]any{"tokens_per_min": tpm},
			At:        time.Now().UTC(),
		}, nil
	}
	return nil, nil
}

// CheckRepoHealth fires when any individual repo breaches either the
// open-PR cap or the CI failure-rate threshold. Per-repo evaluation is
// deliberate: the fleet-wide rate is 0.23% but clawta alone is ~17%, so
// a fleet-averaged threshold would never trip on the one repo that is
// actually bleeding.
func (b *Breaker) CheckRepoHealth(ctx context.Context) (*Trip, error) {
	t := b.thresholds
	if t.MaxOpenPRsPerRepo <= 0 && t.MaxCIFailureRate <= 0 {
		return nil, nil
	}
	stats, err := b.source.RepoHealth(ctx, t.CIWindow)
	if err != nil {
		return nil, fmt.Errorf("repo health: %w", err)
	}
	// Iterate repos in sorted order so the trip sample is deterministic
	// across runs even when multiple repos are over threshold. Open-PR
	// breach takes priority over CI-failure breach, matching the field
	// ordering in Thresholds.
	repos := make([]string, 0, len(stats))
	for repo := range stats {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		s := stats[repo]
		if t.MaxOpenPRsPerRepo > 0 && s.OpenPRs > t.MaxOpenPRsPerRepo {
			return &Trip{
				Signal:    SignalRepoHealth,
				Threshold: fmt.Sprintf("open_prs>%d", t.MaxOpenPRsPerRepo),
				Sample:    map[string]any{"repo": repo, "open_prs": s.OpenPRs},
				At:        time.Now().UTC(),
			}, nil
		}
		if t.MaxCIFailureRate > 0 && s.CIFailureRate > t.MaxCIFailureRate {
			return &Trip{
				Signal:    SignalRepoHealth,
				Threshold: fmt.Sprintf("ci_failure_rate>%.2f", t.MaxCIFailureRate),
				Sample:    map[string]any{"repo": repo, "ci_failure_rate": s.CIFailureRate},
				At:        time.Now().UTC(),
			}, nil
		}
	}
	return nil, nil
}

// CheckTelemetryIntegrity fires when the fraction of recent events
// carrying all required fields drops below MinRequiredFieldCoverage. This
// is the "governance layer going blind" signal — if agents are writing
// events without agent_id/session_id/decision_trace, every downstream
// detection pass is reading shadows.
func (b *Breaker) CheckTelemetryIntegrity(ctx context.Context) (*Trip, error) {
	t := b.thresholds
	if t.MinRequiredFieldCoverage <= 0 {
		return nil, nil
	}
	coverage, sampled, err := b.source.TelemetryCoverage(ctx, t.TelemetryWindow)
	if err != nil {
		return nil, fmt.Errorf("telemetry coverage: %w", err)
	}
	if sampled == 0 {
		return nil, nil
	}
	if coverage < t.MinRequiredFieldCoverage {
		return &Trip{
			Signal:    SignalTelemetryIntegrity,
			Threshold: fmt.Sprintf("field_coverage<%.2f", t.MinRequiredFieldCoverage),
			Sample:    map[string]any{"coverage": coverage, "sampled": sampled},
			At:        time.Now().UTC(),
		}, nil
	}
	return nil, nil
}

// (trip helper removed — Check now performs the compare-and-set + emit
// inline so the open-check, state mutation, and emit decision share one
// critical section. See Check() above.)
