package analyzer

import (
	"fmt"
	"math"
	"time"

	"github.com/AgentGuardHQ/sentinel/internal/config"
)

// DetectDrift analyzes behavioral drift by comparing current action patterns
// to historical baselines. It detects significant changes in:
// 1. Action distribution (which tools agents are using)
// 2. Outcome distribution (allow/deny rates per action)
// 3. Temporal patterns (when agents are active)
func DetectDrift(
	currentEvents []Event,
	baselineEvents []Event,
	cfg config.DriftConfig,
) []Finding {
	if len(currentEvents) == 0 || len(baselineEvents) == 0 {
		return nil
	}

	var findings []Finding

	// 1. Analyze action distribution drift
	actionDrift := detectActionDistributionDrift(currentEvents, baselineEvents, cfg)
	findings = append(findings, actionDrift...)

	// 2. Analyze outcome distribution drift
	outcomeDrift := detectOutcomeDistributionDrift(currentEvents, baselineEvents, cfg)
	findings = append(findings, outcomeDrift...)

	// 3. Analyze temporal pattern drift
	temporalDrift := detectTemporalPatternDrift(currentEvents, baselineEvents, cfg)
	findings = append(findings, temporalDrift...)

	return findings
}

// detectActionDistributionDrift compares which actions/tools are being used
// in current vs baseline periods
func detectActionDistributionDrift(current, baseline []Event, cfg config.DriftConfig) []Finding {
	currentDist := computeActionDistribution(current)
	baselineDist := computeActionDistribution(baseline)

	var findings []Finding
	for action, currentPct := range currentDist {
		baselinePct, hasBaseline := baselineDist[action]
		if !hasBaseline {
			baselinePct = 0.0
		}

		// Calculate percentage point change
		change := math.Abs(currentPct - baselinePct)
		if change > cfg.ActionDistributionThreshold {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("drift-action-%s-%d", action, time.Now().Unix()),
				Pass:     "drift",
				PolicyID: action,
				Metrics: Metrics{
					Count:        countEventsByAction(current, action),
					Rate:         currentPct,
					BaselineRate: baselinePct,
					Deviation:    change,
					SampleSize:   len(current),
				},
				Evidence:   sampleEventsByAction(current, action, 5),
				DetectedAt: time.Now(),
			})
		}
	}

	return findings
}

// detectOutcomeDistributionDrift compares allow/deny rates per action
func detectOutcomeDistributionDrift(current, baseline []Event, cfg config.DriftConfig) []Finding {
	currentOutcomes := computeOutcomeDistribution(current)
	baselineOutcomes := computeOutcomeDistribution(baseline)

	var findings []Finding
	for action, currentRate := range currentOutcomes {
		baselineRate, hasBaseline := baselineOutcomes[action]
		if !hasBaseline {
			baselineRate = 0.0
		}

		// Calculate percentage point change in denial rate
		change := math.Abs(currentRate - baselineRate)
		if change > cfg.OutcomeDistributionThreshold {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("drift-outcome-%s-%d", action, time.Now().Unix()),
				Pass:     "drift",
				PolicyID: action,
				Metrics: Metrics{
					Count:        countEventsByAction(current, action),
					Rate:         currentRate,
					BaselineRate: baselineRate,
					Deviation:    change,
					SampleSize:   len(current),
				},
				Evidence:   sampleEventsByAction(current, action, 5),
				DetectedAt: time.Now(),
			})
		}
	}

	return findings
}

// detectTemporalPatternDrift compares when events occur (hour of day)
func detectTemporalPatternDrift(current, baseline []Event, cfg config.DriftConfig) []Finding {
	currentTemporal := computeTemporalDistribution(current)
	baselineTemporal := computeTemporalDistribution(baseline)

	var findings []Finding
	for hour := 0; hour < 24; hour++ {
		currentPct := currentTemporal[hour]
		baselinePct := baselineTemporal[hour]

		change := math.Abs(currentPct - baselinePct)
		if change > cfg.TemporalDistributionThreshold {
			findings = append(findings, Finding{
				ID:       fmt.Sprintf("drift-temporal-%02d-%d", hour, time.Now().Unix()),
				Pass:     "drift",
				PolicyID: fmt.Sprintf("hour-%02d", hour),
				Metrics: Metrics{
					Count:        countEventsByHour(current, hour),
					Rate:         currentPct,
					BaselineRate: baselinePct,
					Deviation:    change,
					SampleSize:   len(current),
				},
				Evidence:   sampleEventsByHour(current, hour, 5),
				DetectedAt: time.Now(),
			})
		}
	}

	return findings
}

// Helper functions

func computeActionDistribution(events []Event) map[string]float64 {
	total := len(events)
	if total == 0 {
		return make(map[string]float64)
	}

	counts := make(map[string]int)
	for _, e := range events {
		counts[e.Action]++
	}

	distribution := make(map[string]float64)
	for action, count := range counts {
		distribution[action] = float64(count) / float64(total)
	}

	return distribution
}

func computeOutcomeDistribution(events []Event) map[string]float64 {
	// Group by action, compute denial rate per action
	actionDenials := make(map[string]int)
	actionTotals := make(map[string]int)

	for _, e := range events {
		actionTotals[e.Action]++
		if e.Outcome == "deny" {
			actionDenials[e.Action]++
		}
	}

	distribution := make(map[string]float64)
	for action, total := range actionTotals {
		denials := actionDenials[action]
		distribution[action] = float64(denials) / float64(total)
	}

	return distribution
}

func computeTemporalDistribution(events []Event) map[int]float64 {
	total := len(events)
	if total == 0 {
		return make(map[int]float64)
	}

	hourlyCounts := make(map[int]int)
	for _, e := range events {
		hour := e.Timestamp.Hour()
		hourlyCounts[hour]++
	}

	distribution := make(map[int]float64)
	for hour := 0; hour < 24; hour++ {
		count := hourlyCounts[hour]
		distribution[hour] = float64(count) / float64(total)
	}

	return distribution
}

func countEventsByAction(events []Event, action string) int {
	count := 0
	for _, e := range events {
		if e.Action == action {
			count++
		}
	}
	return count
}

func countEventsByHour(events []Event, hour int) int {
	count := 0
	for _, e := range events {
		if e.Timestamp.Hour() == hour {
			count++
		}
	}
	return count
}

func sampleEventsByAction(events []Event, action string, max int) []Event {
	var sampled []Event
	for _, e := range events {
		if e.Action == action {
			sampled = append(sampled, e)
			if len(sampled) >= max {
				break
			}
		}
	}
	return sampled
}

func sampleEventsByHour(events []Event, hour int, max int) []Event {
	var sampled []Event
	for _, e := range events {
		if e.Timestamp.Hour() == hour {
			sampled = append(sampled, e)
			if len(sampled) >= max {
				break
			}
		}
	}
	return sampled
}