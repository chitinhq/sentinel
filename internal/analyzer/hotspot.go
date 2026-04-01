package analyzer

import (
	"fmt"
	"sort"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/db"
)

// DetectHotspots ranks actions by denial volume. Actions with zero denials are excluded.
func DetectHotspots(counts []db.ActionCount) []Finding {
	type actionStats struct {
		denials int
		total   int
	}
	byAction := make(map[string]*actionStats)

	for _, c := range counts {
		s, ok := byAction[c.Action]
		if !ok {
			s = &actionStats{}
			byAction[c.Action] = s
		}
		s.total += c.Count
		if c.Outcome == "deny" {
			s.denials += c.Count
		}
	}

	var findings []Finding
	for action, s := range byAction {
		if s.denials == 0 {
			continue
		}
		rate := float64(s.denials) / float64(s.total)
		findings = append(findings, Finding{
			ID:       fmt.Sprintf("hotspot-%s-%d", action, time.Now().Unix()),
			Pass:     "hotspot",
			PolicyID: action,
			Metrics: Metrics{
				Count:      s.denials,
				Rate:       rate,
				SampleSize: s.total,
			},
			DetectedAt: time.Now(),
		})
	}

	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Metrics.Count > findings[j].Metrics.Count
	})

	return findings
}
