package analyzer

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// lifecycleStatus is one of "started", "completed", "failed", or "" if the
// event doesn't participate in a flow lifecycle.
type lifecycleStatus string

const (
	lifecycleStarted   lifecycleStatus = "started"
	lifecycleCompleted lifecycleStatus = "completed"
	lifecycleFailed    lifecycleStatus = "failed"
)

// flowLifecycle extracts (base flow name, status) from an event. It supports
// two on-wire conventions so the pass works regardless of which emitter fed
// the pipeline:
//
//  1. Dotted-suffix form: Action = "flow.<name>.started"/".completed"/".failed".
//     Strip the trailing suffix; the base is everything before.
//  2. Chitin hook form (see internal/flow/flow.go + ingestion/chitin_governance.go):
//     Action = "flow.<name>" (the tool), and Metadata["action"] =
//     "flow_started"/"flow_completed"/"flow_failed".
//
// Returns ("", "") if the event is not a flow lifecycle event.
func flowLifecycle(e Event) (string, lifecycleStatus) {
	// Form (1): dotted suffix on Action itself.
	if strings.HasSuffix(e.Action, ".started") {
		return strings.TrimSuffix(e.Action, ".started"), lifecycleStarted
	}
	if strings.HasSuffix(e.Action, ".completed") {
		return strings.TrimSuffix(e.Action, ".completed"), lifecycleCompleted
	}
	if strings.HasSuffix(e.Action, ".failed") {
		return strings.TrimSuffix(e.Action, ".failed"), lifecycleFailed
	}

	// Form (2): chitin hook / flow.Emit schema. Tool is the base name
	// (carried through Action), lifecycle lives in metadata["action"].
	if !strings.HasPrefix(e.Action, "flow.") {
		return "", ""
	}
	if e.Metadata == nil {
		return "", ""
	}
	meta, _ := e.Metadata["action"].(string)
	switch meta {
	case "flow_started":
		return e.Action, lifecycleStarted
	case "flow_completed":
		return e.Action, lifecycleCompleted
	case "flow_failed":
		return e.Action, lifecycleFailed
	}
	return "", ""
}

// DetectUnacked finds flow.X.started events with no matching .completed or
// .failed within TTL. Pattern catches dispatch-to-dead-target: caller
// reported success at boundary N without receipt from boundary N+1.
//
// Algorithm:
//  1. Bucket events by (base flow name, session_id).
//  2. Walk each bucket in timestamp order tracking a FIFO of open starts.
//  3. A completed/failed closes the oldest open start for that bucket.
//  4. Starts still open at end of input whose age > ttl become a finding.
//  5. One Finding per (flow_base, session_id), carrying the unacked count
//     and the oldest unacked start as evidence.
func DetectUnacked(events []Event, ttl time.Duration) []Finding {
	type key struct {
		flowBase  string
		sessionID string
	}

	buckets := make(map[key][]Event)
	for _, e := range events {
		base, status := flowLifecycle(e)
		if base == "" || status == "" {
			continue
		}
		k := key{flowBase: base, sessionID: e.SessionID}
		buckets[k] = append(buckets[k], e)
	}

	now := time.Now()
	var findings []Finding

	for k, bucket := range buckets {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].Timestamp.Before(bucket[j].Timestamp)
		})

		// FIFO of open starts. We use a slice as a queue.
		var open []Event
		for _, ev := range bucket {
			_, status := flowLifecycle(ev)
			switch status {
			case lifecycleStarted:
				open = append(open, ev)
			case lifecycleCompleted, lifecycleFailed:
				if len(open) > 0 {
					open = open[1:]
				}
				// If there are no opens, this ack has no matching start —
				// ignore it. Pre-window or duplicate acks are not findings.
			}
		}

		// Filter to opens whose age exceeds ttl.
		var stale []Event
		for _, s := range open {
			if now.Sub(s.Timestamp) > ttl {
				stale = append(stale, s)
			}
		}
		if len(stale) == 0 {
			continue
		}

		// Evidence: oldest unacked first (capped at 10).
		evidence := stale
		if len(evidence) > 10 {
			evidence = evidence[:10]
		}
		oldest := stale[0].Timestamp

		findings = append(findings, Finding{
			ID:       fmt.Sprintf("unacked-%s-%s-%d", k.flowBase, k.sessionID, oldest.Unix()),
			Pass:     "unacked",
			PolicyID: k.flowBase,
			Metrics: Metrics{
				Count:      len(stale),
				SampleSize: len(bucket),
			},
			Evidence:   evidence,
			DetectedAt: now,
		})
	}

	// Deterministic ordering: highest unacked count first, tie-break on key.
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Metrics.Count != findings[j].Metrics.Count {
			return findings[i].Metrics.Count > findings[j].Metrics.Count
		}
		if findings[i].PolicyID != findings[j].PolicyID {
			return findings[i].PolicyID < findings[j].PolicyID
		}
		return findings[i].ID < findings[j].ID
	})

	return findings
}
