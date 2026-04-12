package ingestion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ChitinRuntimeAdapter ingests events from the chitin runtime layer —
// sessions, gates, and souls — into Sentinel's ExecutionEvent store.
// These are distinct from the existing ChitinGovernanceAdapter which
// handles per-repo `.chitin/events.jsonl` files (kernel tool-call
// events); the runtime layer lives in per-user state directories.
//
// Three log files are consumed:
//
//	session-events.log  — session lifecycle (started, ended, rated)
//	soul-events.log     — soul activations / deactivations
//	gate-events.log     — gate pass/fail/error
//
// All three share session_id as the join key, so downstream detection
// passes can correlate a soul activation with subsequent gate results
// and final session rating — the soul experiment's core question.
type ChitinRuntimeAdapter struct {
	// StateDir is the root chitin state directory. Usually
	// $XDG_STATE_HOME/chitin or ~/.local/state/chitin.
	StateDir string

	// ShareDir holds gate-events.log. Defaults to
	// ~/.local/share/chitin. Distinct from StateDir because
	// gate runs are not per-session state in the same sense.
	ShareDir string
}

// NewChitinRuntimeAdapter constructs the adapter with explicit paths.
// Callers in production use config-resolved paths; tests can pass
// tempdirs.
func NewChitinRuntimeAdapter(stateDir, shareDir string) *ChitinRuntimeAdapter {
	return &ChitinRuntimeAdapter{StateDir: stateDir, ShareDir: shareDir}
}

// Ingest reads new events from all three chitin runtime logs since
// the previous checkpoint. Offsets are tracked per file in the
// checkpoint's LastRunID, using the same "path:offset,path:offset"
// format as ChitinGovernanceAdapter for consistency.
func (a *ChitinRuntimeAdapter) Ingest(ctx context.Context, cp *Checkpoint) ([]ExecutionEvent, *Checkpoint, error) {
	offsets := checkpointOffsets{}
	if cp != nil {
		offsets = parseOffsets(cp.LastRunID)
	}

	paths := a.logPaths()
	var all []ExecutionEvent
	for _, p := range paths {
		events, newOffset, err := a.readRuntimeLog(p, offsets[p])
		if err != nil {
			continue // missing/unreadable logs are non-fatal
		}
		all = append(all, events...)
		offsets[p] = newOffset
	}

	newCp := &Checkpoint{
		Adapter:   "chitin_runtime",
		LastRunID: offsets.String(),
		LastRunAt: time.Now(),
	}
	return all, newCp, nil
}

// logPaths returns the three chitin runtime log files in a stable
// order so checkpoints stay consistent across runs.
func (a *ChitinRuntimeAdapter) logPaths() []string {
	return []string{
		filepath.Join(a.StateDir, "session-events.log"),
		filepath.Join(a.StateDir, "soul-events.log"),
		filepath.Join(a.ShareDir, "gate-events.log"),
	}
}

// readRuntimeLog streams JSONL events from path, starting at offset,
// and converts them to ExecutionEvents. The conversion inspects the
// "event" field to dispatch to the right mapper — all three log
// types share enough structure to read with a single loop.
func (a *ChitinRuntimeAdapter) readRuntimeLog(path string, offset int64) ([]ExecutionEvent, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return nil, offset, err
		}
	}

	var events []ExecutionEvent
	seq := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		ev, ok := toExecutionEvent(raw, seq)
		if !ok {
			continue
		}
		events = append(events, ev)
		seq++
	}
	if err := scanner.Err(); err != nil {
		return events, offset, err
	}

	// Compute the new offset by seeking to current position. Using
	// Seek(0, 1) avoids re-reading the file just to count bytes.
	newOffset, err := f.Seek(0, 1)
	if err != nil {
		return events, offset, err
	}
	return events, newOffset, nil
}

// toExecutionEvent maps a single JSONL record to an ExecutionEvent.
// The three log types (session, soul, gate) each have a different
// set of fields, but we can unify them via the "event" field and
// route accordingly. Returns (event, false) for records we don't
// recognize so the reader can skip them.
func toExecutionEvent(raw map[string]any, seq int) (ExecutionEvent, bool) {
	name, _ := raw["event"].(string)
	if name == "" {
		// Gate events use a different shape — their "event" field
		// is actually in the Result struct; try those keys.
		if g, ok := raw["gate"].(string); ok && g != "" {
			return gateToEvent(raw, seq), true
		}
		return ExecutionEvent{}, false
	}

	switch {
	case strings.HasPrefix(name, "session_"):
		return sessionToEvent(raw, name, seq), true
	case strings.HasPrefix(name, "soul_"):
		return soulToEvent(raw, name, seq), true
	default:
		return ExecutionEvent{}, false
	}
}

// sessionToEvent handles session_started, session_ended, session_rated.
// Rating events get an exit_code matching the rating ("good" = 0,
// "bad" = 1, "mixed" = 2) so Sentinel's existing denial-rate passes
// can score soul/driver combinations without knowing our schema.
func sessionToEvent(raw map[string]any, name string, seq int) ExecutionEvent {
	ts := parseTimestamp(raw["ts"])
	sid, _ := raw["session_id"].(string)

	tags := map[string]string{}
	for _, k := range []string{"driver", "model", "soul", "role", "by"} {
		if v, ok := raw[k].(string); ok && v != "" {
			tags[k] = v
		}
	}

	ev := ExecutionEvent{
		ID:          fmt.Sprintf("cr-%s-%s-%d", sid, name, seq),
		Timestamp:   ts,
		Source:      SourceChitinRuntime,
		SessionID:   sid,
		SequenceNum: seq,
		Actor:       ActorAgent,
		AgentID:     tags["driver"],
		Command:     name,
		Tags:        tags,
	}

	// Session rating: treat as ExitCode signal so downstream
	// detection passes can score outcomes.
	if name == "session_rated" {
		if rating, ok := raw["rating"].(string); ok {
			tags["rating"] = rating
			code := ratingExitCode(rating)
			ev.ExitCode = &code
			ev.HasError = code != 0
		}
		if note, ok := raw["note"].(string); ok {
			tags["note"] = note
		}
	}

	// Session ended carries duration_ms.
	if name == "session_ended" {
		if dms, ok := raw["duration_ms"].(float64); ok {
			d := int64(dms)
			ev.DurationMs = &d
		}
		if reason, ok := raw["reason"].(string); ok {
			tags["reason"] = reason
			// Wrapper exit codes are embedded in the reason text —
			// e.g. "wrapper_exit_rc1" → exit code 1. Parse so the
			// stats match actual outcomes.
			if code, ok := parseExitFromReason(reason); ok {
				ev.ExitCode = &code
				ev.HasError = code != 0
			}
		}
	}

	return ev
}

// soulToEvent handles soul_activated, soul_deactivated.
func soulToEvent(raw map[string]any, name string, seq int) ExecutionEvent {
	ts := parseTimestamp(raw["ts"])
	// Soul events don't have session_id (they're user-level toggles)
	// so we use the soul name as a pseudo-session for grouping.
	soul, _ := raw["soul"].(string)

	tags := map[string]string{"soul": soul}
	if by, ok := raw["by"].(string); ok {
		tags["by"] = by
	}
	if targets, ok := raw["targets"].([]any); ok {
		targetStrs := make([]string, 0, len(targets))
		for _, t := range targets {
			if s, ok := t.(string); ok {
				targetStrs = append(targetStrs, s)
			}
		}
		tags["targets"] = strings.Join(targetStrs, ",")
	}

	return ExecutionEvent{
		ID:          fmt.Sprintf("cr-soul-%s-%d", soul, seq),
		Timestamp:   ts,
		Source:      SourceChitinRuntime,
		SessionID:   "soul:" + soul, // prefix to keep session_id space clean
		SequenceNum: seq,
		Actor:       ActorHuman, // souls toggle from user action, not agent
		Command:     name,
		Tags:        tags,
	}
}

// gateToEvent handles gate_result records emitted by chitin gate run.
// Gate result maps to ExitCode (pass=0, fail=1, error=2) so existing
// Sentinel analyses (denial rates, bypass patterns) can score gate
// behavior alongside governance events.
func gateToEvent(raw map[string]any, seq int) ExecutionEvent {
	ts := parseTimestamp(raw["ts"])
	sid, _ := raw["session_id"].(string)
	gate, _ := raw["gate"].(string)
	name, _ := raw["name"].(string)
	result, _ := raw["result"].(string)
	reason, _ := raw["reason"].(string)
	repo, _ := raw["repo"].(string)
	issue, _ := raw["issue"].(string)
	queue, _ := raw["queue"].(string)

	exitCode := 2 // fail-closed default
	switch result {
	case "pass":
		exitCode = 0
	case "fail":
		exitCode = 1
	}

	tags := map[string]string{
		"gate":   gate,
		"name":   name,
		"result": result,
	}
	if reason != "" {
		tags["reason"] = reason
	}
	if issue != "" {
		tags["issue"] = issue
	}
	if queue != "" {
		tags["queue"] = queue
	}

	return ExecutionEvent{
		ID:          fmt.Sprintf("cr-gate-%s-%d", name, seq),
		Timestamp:   ts,
		Source:      SourceChitinRuntime,
		SessionID:   sid,
		SequenceNum: seq,
		Actor:       ActorAgent,
		Command:     "gate.result",
		ExitCode:    &exitCode,
		HasError:    exitCode != 0,
		Repository:  repo,
		Tags:        tags,
	}
}

// ratingExitCode maps a user rating to an ExitCode so Sentinel's
// outcome-based analyses score soul/driver combinations with the
// same instruments they use for governance outcomes.
func ratingExitCode(rating string) int {
	switch rating {
	case "good":
		return 0
	case "bad":
		return 1
	default: // "mixed" or unknown
		return 2
	}
}

// parseExitFromReason extracts an exit code from wrapper_exit_rcN
// reason strings. Returns (0, false) if the reason doesn't match.
func parseExitFromReason(reason string) (int, bool) {
	const prefix = "wrapper_exit_rc"
	if !strings.HasPrefix(reason, prefix) {
		return 0, false
	}
	var code int
	if _, err := fmt.Sscanf(reason[len(prefix):], "%d", &code); err != nil {
		return 0, false
	}
	return code, true
}

// parseTimestamp tolerates both RFC3339Nano (what chitin emits) and
// raw epoch numbers (just in case). Returns zero time on failure,
// which downstream code should handle.
func parseTimestamp(v any) time.Time {
	switch tv := v.(type) {
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, tv); err == nil {
				return t
			}
		}
	case float64:
		return time.Unix(int64(tv), 0)
	}
	return time.Time{}
}
