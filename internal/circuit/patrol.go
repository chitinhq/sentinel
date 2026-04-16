// Package circuit — patrol.go wires the Breaker into a long-running
// goroutine. It is deliberately separated from breaker.go so the
// stateless trip logic stays trivially testable while the lifecycle
// (ticker, ctx cancellation, optional auto-recheck) lives here.
//
// Architecture: orthogonal patrol, never inline. The patrol calls
// Breaker.Check on a fixed interval and lets the Emitter put the
// circuit.<signal> event onto the shared events.jsonl stream. Octi's
// dispatcher tails that stream — the patrol never calls into Octi.
package circuit

import (
	"context"
	"log"
	"time"
)

// Patrol is a periodic Check runner. The zero value is unusable; build
// with NewPatrol.
type Patrol struct {
	breaker  *Breaker
	interval time.Duration
	logger   *log.Logger
}

// NewPatrol returns a patrol that calls b.Check every interval. A zero
// interval defaults to 60s. logger may be nil (logging is suppressed).
func NewPatrol(b *Breaker, interval time.Duration, logger *log.Logger) *Patrol {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Patrol{breaker: b, interval: interval, logger: logger}
}

// Run blocks until ctx is done, calling Breaker.Check every interval.
// One Check fires immediately on entry so the first trip is not delayed
// a full interval after startup. Errors from Check are logged and
// swallowed — a transient Neon hiccup must not kill the patrol.
func (p *Patrol) Run(ctx context.Context) error {
	if p.breaker == nil {
		return nil
	}
	p.tick(ctx)

	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			p.tick(ctx)
		}
	}
}

func (p *Patrol) tick(ctx context.Context) {
	trip, err := p.breaker.Check(ctx)
	if err != nil {
		p.logf("circuit patrol: check error: %v", err)
		return
	}
	if trip != nil && p.logger != nil {
		p.logf("circuit patrol: TRIPPED signal=%s threshold=%s sample=%v",
			trip.Signal, trip.Threshold, trip.Sample)
	}
}

func (p *Patrol) logf(format string, args ...any) {
	if p.logger == nil {
		return
	}
	p.logger.Printf(format, args...)
}
