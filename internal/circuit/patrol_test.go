package circuit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// patrolFakeSource lets us synthesize a tripped retry-storm signal.
type patrolFakeSource struct {
	mu          sync.Mutex
	retryCounts map[string]int
}

func (f *patrolFakeSource) RetryCounts(ctx context.Context, window time.Duration) (map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.retryCounts))
	for k, v := range f.retryCounts {
		out[k] = v
	}
	return out, nil
}
func (f *patrolFakeSource) ActiveAgents(ctx context.Context) (int, time.Duration, float64, error) {
	return 0, 0, 0, nil
}
func (f *patrolFakeSource) RepoHealth(ctx context.Context, window time.Duration) (map[string]RepoStats, error) {
	return nil, nil
}
func (f *patrolFakeSource) TelemetryCoverage(ctx context.Context, window time.Duration) (float64, int, error) {
	return 1.0, 100, nil
}

type patrolRecEmitter struct {
	mu     sync.Mutex
	events []string
}

func (r *patrolRecEmitter) Emit(_ context.Context, name string, _ map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, name)
}
func (r *patrolRecEmitter) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.events...)
	return out
}

func TestPatrolEmitsOnTrip(t *testing.T) {
	src := &patrolFakeSource{retryCounts: map[string]int{"task-a": 99}}
	emit := &patrolRecEmitter{}
	b := New(DefaultThresholds(), src, emit)

	p := NewPatrol(b, 10*time.Millisecond, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = p.Run(ctx)

	got := emit.names()
	if len(got) == 0 {
		t.Fatalf("expected at least one emitted circuit event, got none")
	}
	if got[0] != "circuit.retry_storm" {
		t.Fatalf("expected first event circuit.retry_storm, got %q", got[0])
	}
	open, trip := b.State()
	if !open || trip == nil || trip.Signal != SignalRetryStorm {
		t.Fatalf("expected breaker open on retry_storm, got open=%v trip=%+v", open, trip)
	}
}

func TestPatrolHonorsCancellation(t *testing.T) {
	src := &patrolFakeSource{retryCounts: nil}
	b := New(DefaultThresholds(), src, &patrolRecEmitter{})
	p := NewPatrol(b, 1*time.Second, nil)

	var done atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = p.Run(ctx)
		done.Store(true)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if done.Load() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("patrol did not exit after cancel")
}
