package analyzer

import (
	"fmt"
	"sort"
	"time"

	"github.com/chitinhq/sentinel/internal/config"
)

// DetectBypassPatterns finds agents that repeatedly retry a denied action
// within a sliding time window, which may indicate attempts to bypass policy.
func DetectBypassPatterns(events []Event, cfg config.BypassConfig) []Finding {
	var denials []Event
	for _, e := range events {
		if e.Outcome == "deny" {
			denials = append(denials, e)
		}
	}
	sort.Slice(denials, func(i, j int) bool {
		return denials[i].Timestamp.Before(denials[j].Timestamp)
	})

	type groupKey struct{ AgentID, Action string }
	groups := make(map[groupKey][]Event)
	for _, d := range denials {
		k := groupKey{AgentID: d.AgentID, Action: d.Action}
		groups[k] = append(groups[k], d)
	}

	var findings []Finding
	for k, evts := range groups {
		for i := 0; i < len(evts); i++ {
			windowEnd := evts[i].Timestamp.Add(cfg.Window)
			count := 0
			var windowEvents []Event
			for j := i; j < len(evts) && evts[j].Timestamp.Before(windowEnd); j++ {
				count++
				windowEvents = append(windowEvents, evts[j])
			}
			if count > cfg.MinRetries {
				evidence := windowEvents
				if len(evidence) > 10 {
					evidence = evidence[:10]
				}
				findings = append(findings, Finding{
					ID:         fmt.Sprintf("bypass-%s-%s-%d", k.AgentID, k.Action, evts[i].Timestamp.Unix()),
					Pass:       "bypass",
					PolicyID:   k.Action,
					Metrics:    Metrics{Count: count, SampleSize: count},
					Evidence:   evidence,
					DetectedAt: time.Now(),
				})
				i += count - 1
				break
			}
		}
	}
	return findings
}
