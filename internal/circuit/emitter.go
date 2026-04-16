package circuit

import (
	"context"

	"github.com/chitinhq/sentinel/internal/flow"
)

// FlowEmitter is the production Emitter. It publishes circuit trips onto
// the same events.jsonl stream that governance + flow events already use,
// so Octi's dispatcher (which tails the stream) can subscribe without any
// new transport.
type FlowEmitter struct{}

// Emit writes a flow_failed event so the analyzer's existing per-flow
// health view picks it up automatically. The Breaker passes the fully-
// qualified event name ("circuit.<signal>") — FlowEmitter is a thin
// pass-through and does NOT prefix again (doing so produced
// "circuit.circuit.<signal>" in an earlier draft).
func (FlowEmitter) Emit(_ context.Context, name string, detail map[string]any) {
	flow.Fail(name, detail)
}
