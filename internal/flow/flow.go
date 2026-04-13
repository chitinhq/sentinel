// Package flow lets any chitin-ecosystem component emit start/complete/fail
// events for a named flow (an SDLC gate, a dispatch step, a pipeline stage)
// into the same events.jsonl stream that governance events land in. Sentinel
// ingests them, the analyzer sees them, and `sentinel flows` rolls them up
// into a per-flow health view.
//
// Events-as-health: there is no separate dashboard schema. A flow is healthy
// when its latest `completed` is more recent than its latest `failed`, the
// same way hotspot/toolrisk reason about governance decisions today.
//
// Usage:
//
//	flow.Emit("sentinel.analyze.pass.hotspot", "started", nil)
//	// ... do the work ...
//	flow.Emit("sentinel.analyze.pass.hotspot", "completed", map[string]any{
//	    "findings": 3,
//	})
//
// The wire format matches chitin's events.jsonl schema so no ingester changes
// are needed:
//
//	{
//	  "ts": "...",
//	  "sid": "...",
//	  "agent": "...",
//	  "tool": "flow.<name>",     // sentinel analyzers partition on action prefix
//	  "action": "flow_<status>", // "flow_started"|"flow_completed"|"flow_failed"
//	  "outcome": "allow"|"deny",
//	  "reason": "<optional>",
//	  "source": "flow",
//	  "latency_us": 0,
//	  "fields": {...optional metadata...}
//	}
package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status is one of the three lifecycle points of a flow.
type Status string

const (
	Started   Status = "started"
	Completed Status = "completed"
	Failed    Status = "failed"
)

// event is the on-disk JSONL shape, aligned with chitin events.jsonl + mcptrace.
type event struct {
	Timestamp string                 `json:"ts"`
	SessionID string                 `json:"sid,omitempty"`
	Agent     string                 `json:"agent"`
	Tool      string                 `json:"tool"`
	Action    string                 `json:"action"`
	Outcome   string                 `json:"outcome"`
	Reason    string                 `json:"reason,omitempty"`
	Source    string                 `json:"source"`
	LatencyUs int64                  `json:"latency_us"`
	Fields    map[string]any         `json:"fields,omitempty"`
}

var writeMu sync.Mutex

// Emit writes a single flow lifecycle event.
//
// name is the dotted flow identifier, e.g. "sentinel.analyze.pass.hotspot".
// status is Started/Completed/Failed (Completed+Failed both acceptable
// terminal states).
// fields is optional structured metadata persisted into the event.
//
// Best-effort: failures to write are swallowed so telemetry never breaks the
// caller. Synchronous file I/O under a write mutex; expected microseconds on
// local disk but could block under I/O contention.
func Emit(name string, status Status, fields map[string]any) {
	path := destination()
	if path == "" {
		return
	}
	reason := ""
	if status == Failed {
		if fields != nil {
			if r, ok := fields["reason"].(string); ok {
				reason = r
			}
		}
	}
	outcome := "allow"
	if status == Failed {
		outcome = "deny"
	}

	ev := event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: os.Getenv("CHITIN_SESSION_ID"),
		Agent:     agentName(),
		Tool:      "flow." + name,
		Action:    "flow_" + string(status),
		Outcome:   outcome,
		Reason:    reason,
		Source:    "flow",
		Fields:    fields,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	data = append(data, '\n')

	writeMu.Lock()
	defer writeMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return
	}
}

// Start/Complete/Fail are convenience wrappers for the common pattern
//
//	flow.Start(name, nil)
//	defer flow.Complete(name, nil)
//	// ... work ...
//	// on error: flow.Fail(name, map[string]any{"reason": err.Error()})
//
// Prefer these over raw Emit for readability at the call site.
func Start(name string, fields map[string]any) { Emit(name, Started, fields) }
func Complete(name string, fields map[string]any) { Emit(name, Completed, fields) }
func Fail(name string, fields map[string]any) { Emit(name, Failed, fields) }

// Span runs fn bracketed by Start/Complete, or Start/Fail if fn returns a
// non-nil error. Returns fn's error unchanged.
//
//	err := flow.Span("sentinel.analyze", nil, func() error {
//	    return doAnalyze()
//	})
func Span(name string, fields map[string]any, fn func() error) error {
	start := time.Now()
	Start(name, fields)
	err := fn()
	elapsed := time.Since(start)
	end := map[string]any{"duration_ms": elapsed.Milliseconds()}
	for k, v := range fields {
		end[k] = v
	}
	if err != nil {
		end["reason"] = err.Error()
		Fail(name, end)
	} else {
		Complete(name, end)
	}
	return err
}

// destination resolves the JSONL path to write to. Reuses the same precedence
// the chitin hook and mcptrace emitters use so all three streams land in
// the same file.
func destination() string {
	if p := os.Getenv("FLOW_EVENTS_FILE"); p != "" {
		return p
	}
	if p := os.Getenv("MCPTRACE_FILE"); p != "" {
		return p
	}
	if ws := os.Getenv("CHITIN_WORKSPACE"); ws != "" {
		return filepath.Join(ws, ".chitin", "events.jsonl")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".chitin", "flow_events.jsonl")
	}
	return ""
}

// agentName picks a stable agent identifier for the caller. Prefers
// explicit CHITIN_AGENT_NAME (set by chitin session wrap and by drivers),
// falls back to a coarse runtime tag.
func agentName() string {
	if a := os.Getenv("CHITIN_AGENT_NAME"); a != "" {
		return a
	}
	if a := os.Getenv("CHITIN_AGENT"); a != "" {
		return a
	}
	return "unknown"
}
