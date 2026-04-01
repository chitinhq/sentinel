package analyzer

import (
	"fmt"
	"sort"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/db"
)

func ProfileToolRisk(rates []db.DenialRate) []Finding {
	var findings []Finding
	for _, r := range rates {
		if r.DenialCount == 0 {
			continue
		}
		findings = append(findings, Finding{
			ID:       fmt.Sprintf("toolrisk-%s-%d", r.Action, time.Now().Unix()),
			Pass:     "tool_risk",
			PolicyID: r.Action,
			Metrics:  Metrics{Count: r.DenialCount, Rate: r.DenialRate, SampleSize: r.TotalCount},
			DetectedAt: time.Now(),
		})
	}
	sort.Slice(findings, func(i, j int) bool {
		return findings[i].Metrics.Rate > findings[j].Metrics.Rate
	})
	return findings
}
