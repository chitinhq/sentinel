package insights

import "time"

// InsightCategory classifies an insight.
type InsightCategory string

const (
	CategoryHealth         InsightCategory = "health"
	CategoryPattern        InsightCategory = "pattern"
	CategoryRecommendation InsightCategory = "recommendation"
	CategoryAnomaly        InsightCategory = "anomaly"
)

// InsightSeverity classifies urgency.
type InsightSeverity string

const (
	SeverityInfo     InsightSeverity = "info"
	SeverityWarning  InsightSeverity = "warning"
	SeverityHigh     InsightSeverity = "high"
	SeverityCritical InsightSeverity = "critical"
)

// Insight is a generated intelligence item.
type Insight struct {
	ID              string          `json:"id"`
	Timestamp       time.Time       `json:"timestamp"`
	Category        InsightCategory `json:"category"`
	Severity        InsightSeverity `json:"severity"`
	Narrative       string          `json:"narrative"`
	Evidence        map[string]any  `json:"evidence,omitempty"`
	SuggestedAction string          `json:"suggested_action,omitempty"`
	ScopeType       string          `json:"scope_type,omitempty"`
	ScopeValue      string          `json:"scope_value,omitempty"`
	Acknowledged    bool            `json:"acknowledged"`
}

// llmInsight is the JSON shape returned by the LLM.
type llmInsight struct {
	Category        string         `json:"category"`
	Severity        string         `json:"severity"`
	Narrative       string         `json:"narrative"`
	Evidence        map[string]any `json:"evidence,omitempty"`
	SuggestedAction string         `json:"suggested_action"`
	ScopeType       string         `json:"scope_type"`
	ScopeValue      string         `json:"scope_value"`
}

// HealthScoreDelta tracks a score change between cycles.
type HealthScoreDelta struct {
	ScopeType  string         `json:"scope_type"`
	ScopeValue string         `json:"scope_value"`
	Score      int            `json:"score"`
	Prior      int            `json:"prior"`
	Delta      int            `json:"delta"`
	Dimensions map[string]int `json:"dimensions"`
}

// GeneratorInputs collects all data needed for insight generation.
type GeneratorInputs struct {
	ScoreDeltas    []HealthScoreDelta     `json:"score_deltas,omitempty"`
	FailureCounts  map[string]int         `json:"failure_counts,omitempty"`  // repo → count
	PlatformStats  map[string]int         `json:"platform_stats,omitempty"` // platform → failures
	SkipListSize   int                    `json:"skip_list_size"`
	DispatchCounts map[string]int         `json:"dispatch_counts,omitempty"` // platform → count
	BudgetPcts     map[string]int         `json:"budget_pcts,omitempty"`     // platform → usage_pct
	EventVolume    int                    `json:"event_volume"`
}
