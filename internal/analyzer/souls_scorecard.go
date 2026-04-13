package analyzer

import (
	"fmt"
	"sort"
	"time"
)

// SoulScorecardPass is the Pass identifier stamped onto Findings emitted
// by ProfileSouls. Callers filtering interpreted findings by pass use
// this constant.
const SoulScorecardPass = "soul_scorecard"

// SoulMetrics holds the per-(soul, stage) axes we can actually compute
// from governance_events today. Fields we can't fill yet (ship_velocity,
// polish_rate) are intentionally absent — see issue #49 for the followups
// that need PR metadata joined in.
//
// It's carried through a Finding inside Metadata (see Finding emission
// below) because Metrics is a fixed numeric struct and we don't want to
// bend its semantics for a richer scorecard.
type SoulMetrics struct {
	Soul          string  `json:"soul"`
	Stage         string  `json:"stage"`
	Sessions      int     `json:"sessions"`
	Events        int     `json:"events"`
	AllowCount    int     `json:"allow_count"`
	SafetyRate    float64 `json:"safety_rate"`     // allow / total
	RatingMean    float64 `json:"rating_mean"`     // avg metadata.rating (0 if none)
	RatingSamples int     `json:"rating_samples"`  // how many events had a rating
	// FindingRate (sentinel findings / session) is deliberately NOT set
	// here — ProfileSouls doesn't see other passes' output. Pipeline can
	// enrich post-hoc; see souls.go CLI for the raw-event side.
}

// ProfileSouls groups events by (metadata.soul, metadata.observed_stage)
// and emits one Finding per pair, scored on the axes we can derive from
// governance_events alone.
//
// Graceful degradation:
//   - Events with no soul in metadata are skipped (not an error).
//   - Empty input → empty output.
//   - Missing observed_stage falls back to "" so unclassified runs still
//     show up as their own row rather than vanishing.
//
// The metadata.soul / metadata.observed_stage keys arrive from chitin#94.
// Until that lands, this pass will correctly emit zero findings.
func ProfileSouls(events []Event, now time.Time) []Finding {
	if len(events) == 0 {
		return nil
	}

	type key struct{ soul, stage string }
	type agg struct {
		events     int
		allow      int
		sessions   map[string]struct{}
		ratingSum  float64
		ratingHits int
	}
	groups := make(map[key]*agg)

	for _, e := range events {
		soul := metaString(e.Metadata, "soul")
		if soul == "" {
			continue
		}
		stage := metaString(e.Metadata, "observed_stage")

		k := key{soul: soul, stage: stage}
		g, ok := groups[k]
		if !ok {
			g = &agg{sessions: make(map[string]struct{})}
			groups[k] = g
		}
		g.events++
		if e.Outcome == "allow" {
			g.allow++
		}
		if e.SessionID != "" {
			g.sessions[e.SessionID] = struct{}{}
		}
		if r, ok := metaFloat(e.Metadata, "rating"); ok {
			g.ratingSum += r
			g.ratingHits++
		}
	}

	if len(groups) == 0 {
		return nil
	}

	out := make([]Finding, 0, len(groups))
	for k, g := range groups {
		sm := SoulMetrics{
			Soul:          k.soul,
			Stage:         k.stage,
			Sessions:      len(g.sessions),
			Events:        g.events,
			AllowCount:    g.allow,
			RatingSamples: g.ratingHits,
		}
		if g.events > 0 {
			sm.SafetyRate = float64(g.allow) / float64(g.events)
		}
		if g.ratingHits > 0 {
			sm.RatingMean = g.ratingSum / float64(g.ratingHits)
		}

		stageLabel := k.stage
		if stageLabel == "" {
			stageLabel = "unknown"
		}
		out = append(out, Finding{
			ID:       fmt.Sprintf("souls-%s-%s-%d", k.soul, stageLabel, now.Unix()),
			Pass:     SoulScorecardPass,
			PolicyID: fmt.Sprintf("%s/%s", k.soul, stageLabel),
			Metrics: Metrics{
				Count:      g.events,
				Rate:       sm.SafetyRate,
				SampleSize: sm.Sessions,
			},
			DetectedAt: now,
			// Sidecar the full scorecard on the first evidence event's
			// metadata map — we don't have a dedicated Metadata field on
			// Finding yet, so we attach it via a synthetic Event so
			// downstream consumers (interpreter, digest) can reach it.
			Evidence: []Event{{
				ID:        fmt.Sprintf("souls-metrics-%s-%s", k.soul, stageLabel),
				Timestamp: now,
				EventType: SoulScorecardPass,
				Metadata: map[string]any{
					"soul_metrics": sm,
				},
			}},
		})
	}

	// Deterministic order: highest session count first, then soul/stage.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Metrics.SampleSize != out[j].Metrics.SampleSize {
			return out[i].Metrics.SampleSize > out[j].Metrics.SampleSize
		}
		return out[i].PolicyID < out[j].PolicyID
	})
	return out
}

// metaString pulls a string value out of a jsonb-style metadata map,
// tolerating nil maps and non-string values.
func metaString(md map[string]any, key string) string {
	if md == nil {
		return ""
	}
	if v, ok := md[key].(string); ok {
		return v
	}
	return ""
}

// metaFloat coerces numeric metadata (raw float, int, json.Number-ish)
// into a float. Returns (_, false) if the key is missing or unparseable.
func metaFloat(md map[string]any, key string) (float64, bool) {
	if md == nil {
		return 0, false
	}
	v, ok := md[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
