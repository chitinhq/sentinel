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

// Emit writes a flow_failed event on the "circuit.<signal>" name so
// the analyzer's existing per-flow health view picks it up automatically.
func (FlowEmitter) Emit(_ context.Context, signal string, detail map[string]any) {
	flow.Fail("circuit."+signal, detail)
}
