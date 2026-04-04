package analyzer

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/config"
	"github.com/AgentGuardHQ/sentinel/internal/db"
)

// DetectCommandFailures identifies commands with high failure rates from
// execution_events data.  Commands below MinOccurrences or FailureRateThreshold
// are filtered out.
func DetectCommandFailures(rates []db.CommandFailureRate, cfg config.CommandFailureConfig) []Finding {
	var findings []Finding
	for _, r := range rates {
		if r.TotalCount < cfg.MinOccurrences {
			continue
		}
		if r.FailureRate < cfg.FailureRateThreshold {
			continue
		}
		id := fmt.Sprintf("cmdfailure-%s-%d", sanitizeID(r.Command), time.Now().Unix())
		findings = append(findings, Finding{
			ID:       id,
			Pass:     "command_failure",
			PolicyID: r.Command,
			Metrics: Metrics{
				Count:      r.FailureCount,
				Rate:       r.FailureRate,
				SampleSize: r.TotalCount,
			},
			DetectedAt: time.Now(),
		})
	}
	return findings
}

// sanitizeID converts a command string into a safe identifier segment:
// lowercase, with non-alphanumeric characters replaced by dashes.
func sanitizeID(s string) string {
	s = strings.ToLower(s)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	return re.ReplaceAllString(s, "-")
}
