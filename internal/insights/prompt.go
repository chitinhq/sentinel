package insights

import (
	"encoding/json"
	"fmt"
)

func healthNarrativeSystemPrompt() string {
	return `You are a swarm telemetry analyst. Given health score data, write a 2-3 sentence narrative explaining why the score changed. Focus on root cause, not symptoms. Respond ONLY with a JSON array — no markdown fencing.

Each element must have: {"category": "health", "severity": "info|warning|high|critical", "narrative": "...", "evidence": {...}, "suggested_action": "...", "scope_type": "platform|repo|queue", "scope_value": "..."}`
}

func patternDetectionSystemPrompt() string {
	return `You are a swarm telemetry analyst. Given failure patterns across repos and platforms, identify recurring patterns with root causes. Respond ONLY with a JSON array — no markdown fencing.

Each element must have: {"category": "pattern", "severity": "info|warning|high|critical", "narrative": "...", "evidence": {...}, "suggested_action": "...", "scope_type": "system", "scope_value": "system"}`
}

func dispatchRecommendationSystemPrompt() string {
	return `You are a swarm dispatch optimizer. Given platform utilization and failure data, suggest improvements to swarm throughput. Respond ONLY with a JSON array — no markdown fencing.

Each element must have: {"category": "recommendation", "severity": "info|warning", "narrative": "...", "evidence": {...}, "suggested_action": "...", "scope_type": "platform|queue", "scope_value": "..."}`
}

func anomalyAlertSystemPrompt() string {
	return `You are a swarm anomaly detector. Given volume and failure spikes, explain whether the anomaly requires action or is expected. Respond ONLY with a JSON array — no markdown fencing.

Each element must have: {"category": "anomaly", "severity": "info|warning|high|critical", "narrative": "...", "evidence": {...}, "suggested_action": "...", "scope_type": "system", "scope_value": "system"}`
}

func buildHealthPrompt(deltas []HealthScoreDelta, failures map[string]int) string {
	data := map[string]any{
		"score_changes":    deltas,
		"failures_by_repo": failures,
	}
	j, _ := json.Marshal(data)
	return fmt.Sprintf("Health score changes and failure data:\n%s", string(j))
}

func buildPatternPrompt(failures map[string]int, platformStats map[string]int) string {
	data := map[string]any{
		"failures_by_repo":     failures,
		"failures_by_platform": platformStats,
	}
	j, _ := json.Marshal(data)
	return fmt.Sprintf("Failure patterns across repos and platforms:\n%s", string(j))
}

func buildRecommendationPrompt(dispatchCounts, budgetPcts map[string]int, platformFailures map[string]int) string {
	data := map[string]any{
		"dispatch_counts":      dispatchCounts,
		"budget_usage_pcts":    budgetPcts,
		"failures_by_platform": platformFailures,
	}
	j, _ := json.Marshal(data)
	return fmt.Sprintf("Platform utilization and failure data:\n%s", string(j))
}

func buildAnomalyPrompt(eventVolume int, avgVolume float64) string {
	data := map[string]any{
		"current_volume":       eventVolume,
		"average_daily_volume": avgVolume,
		"spike_ratio":          0.0,
	}
	if avgVolume > 0 {
		data["spike_ratio"] = float64(eventVolume) / avgVolume
	}
	j, _ := json.Marshal(data)
	return fmt.Sprintf("Volume anomaly data:\n%s", string(j))
}
