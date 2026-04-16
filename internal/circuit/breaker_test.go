package circuit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeSource implements SignalSource with hand-tuned responses so each
// test can synthesize exactly one failure mode.
type fakeSource struct {
	retries      map[string]int
	agents       int
	oldest       time.Duration
	tpm          float64
	repos        map[string]RepoStats
	coverage     float64
	sampled      int
	coverageDone bool
}

func (f *fakeSource) RetryCounts(_ context.Context, _ time.Duration) (map[string]int, error) {
	if f.retries == nil {
		return map[string]int{}, nil
	}
	return f.retries, nil
}
func (f *fakeSource) ActiveAgents(_ context.Context) (int, time.Duration, float64, error) {
	return f.agents, f.oldest, f.tpm, nil
}
func (f *fakeSource) RepoHealth(_ context.Context, _ time.Duration) (map[string]RepoStats, error) {
	if f.repos == nil {
		return map[string]RepoStats{}, nil
	}
	return f.repos, nil
}
func (f *fakeSource) TelemetryCoverage(_ context.Context, _ time.Duration) (float64, int, error) {
	if !f.coverageDone {
		// default: perfectly healthy, many samples
		return 1.0, 100, nil
	}
	return f.coverage, f.sampled, nil
}

// recorder captures Emit() calls.
type recorder struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *recorder) Emit(_ context.Context, signal string, detail map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := map[string]any{"_event": signal}
	for k, v := range detail {
		cp[k] = v
	}
	r.events = append(r.events, cp)
}

func healthyDefaults() Thresholds {
	t := DefaultThresholds()
	return t
}

// TestBreakerClosedUnderHealthyLoad asserts no signal trips when every
// input is well inside threshold. Integration-style.
func TestBreakerClosedUnderHealthyLoad(t *testing.T) {
	src := &fakeSource{
		retries:      map[string]int{"task-a": 1, "task-b": 2},
		agents:       3,
		oldest:       10 * time.Minute,
		tpm:          5000,
		repos:        map[string]RepoStats{"chitinhq/chitin": {OpenPRs: 4, CIFailureRate: 0.01}},
		coverage:     0.99,
		sampled:      200,
		coverageDone: true,
	}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	trip, err := b.Check(context.Background())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if trip != nil {
		t.Fatalf("expected closed, got trip: %+v", trip)
	}
	open, _ := b.State()
	if open {
		t.Fatalf("expected breaker closed")
	}
	if len(rec.events) != 0 {
		t.Fatalf("expected no events, got %d", len(rec.events))
	}
}

func TestRetryStormTrips(t *testing.T) {
	src := &fakeSource{retries: map[string]int{"runaway-task": 7}}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	trip, err := b.Check(context.Background())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if trip == nil || trip.Signal != SignalRetryStorm {
		t.Fatalf("expected retry_storm trip, got %+v", trip)
	}
	if trip.Sample["task_id"] != "runaway-task" {
		t.Fatalf("sample missing task_id: %+v", trip.Sample)
	}
	open, _ := b.State()
	if !open {
		t.Fatalf("expected breaker open after trip")
	}
	if len(rec.events) != 1 || rec.events[0]["_event"] != "circuit.tripped" {
		t.Fatalf("expected 1 circuit.tripped event, got %+v", rec.events)
	}
}

func TestResourceBurnTripsOnSessionDuration(t *testing.T) {
	src := &fakeSource{
		agents: 2,
		oldest: 3 * time.Hour, // exceeds 2h default
		tpm:    100,
	}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	trip, err := b.Check(context.Background())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if trip == nil || trip.Signal != SignalResourceBurn {
		t.Fatalf("expected resource_burn trip, got %+v", trip)
	}
}

func TestRepoHealthTripsPerRepo(t *testing.T) {
	// Fleet-averaged rate would be ~8.5% (well below 15%), but clawta
	// alone is 17% — per-repo check must catch it.
	src := &fakeSource{
		repos: map[string]RepoStats{
			"chitinhq/chitin":  {OpenPRs: 3, CIFailureRate: 0.0},
			"chitinhq/clawta":  {OpenPRs: 4, CIFailureRate: 0.17},
		},
	}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	trip, err := b.Check(context.Background())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if trip == nil || trip.Signal != SignalRepoHealth {
		t.Fatalf("expected repo_health trip, got %+v", trip)
	}
	if trip.Sample["repo"] != "chitinhq/clawta" {
		t.Fatalf("expected clawta to be the culprit, got %+v", trip.Sample)
	}
}

func TestTelemetryIntegrityTrips(t *testing.T) {
	src := &fakeSource{
		coverage:     0.80, // below 0.95 default
		sampled:      500,
		coverageDone: true,
	}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	trip, err := b.Check(context.Background())
	if err != nil {
		t.Fatalf("Check err: %v", err)
	}
	if trip == nil || trip.Signal != SignalTelemetryIntegrity {
		t.Fatalf("expected telemetry_integrity trip, got %+v", trip)
	}
}

func TestResetClosesBreaker(t *testing.T) {
	src := &fakeSource{retries: map[string]int{"x": 9}}
	b := New(healthyDefaults(), src, &recorder{})
	if _, err := b.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if open, _ := b.State(); !open {
		t.Fatal("expected open after trip")
	}
	b.Reset()
	if open, trip := b.State(); open || trip != nil {
		t.Fatalf("expected closed after reset, got open=%v trip=%+v", open, trip)
	}
	// After reset, a second Check with the same offending source trips again.
	trip2, err := b.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if trip2 == nil {
		t.Fatal("expected re-trip after reset")
	}
}

func TestCheckIsNoopWhileOpen(t *testing.T) {
	src := &fakeSource{retries: map[string]int{"x": 9}}
	rec := &recorder{}
	b := New(healthyDefaults(), src, rec)

	if _, err := b.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly one emit while open, got %d", len(rec.events))
	}
}
