package analyzer

import "time"

// Event mirrors a row from governance_events in Neon.
type Event struct {
	ID            string
	Timestamp     time.Time
	AgentID       string
	SessionID     string
	EventType     string // e.g. "tool_call"
	Action        string // tool name: "Bash", "Edit", "Read", etc.
	Resource      string
	Outcome       string // "allow", "deny"
	RiskLevel     string // "low", "medium", "high", "critical"
	PolicyVersion string
	Metadata      map[string]any // parsed JSON metadata
}

// MatchedPolicy extracts the policy identifier from event metadata.
// Returns the action name as fallback if no policy is recorded.
func (e Event) MatchedPolicy() string {
	if e.Metadata != nil {
		if p, ok := e.Metadata["matched_policy"].(string); ok && p != "" {
			return p
		}
		if p, ok := e.Metadata["policy_id"].(string); ok && p != "" {
			return p
		}
		if p, ok := e.Metadata["invariant"].(string); ok && p != "" {
			return p
		}
	}
	return e.Action
}

// Metrics captures hard numbers for a finding.
type Metrics struct {
	Count        int
	Rate         float64 // e.g. denial rate 0.0-1.0
	BaselineRate float64 // 7-day average for comparison
	Deviation    float64 // standard deviations from baseline
	SampleSize   int
}

// Finding is the output of a single detection pass.
type Finding struct {
	ID         string
	Pass       string  // "hotspot", "false_positive", "bypass", "tool_risk", "anomaly"
	PolicyID   string  // matched policy or action name
	Metrics    Metrics
	Evidence   []Event // capped at 10
	DetectedAt time.Time
}

// InterpretedFinding is a Finding enriched by LLM interpretation.
type InterpretedFinding struct {
	Finding        Finding
	Actionable     bool
	Remediation    string
	Novelty        string  // "new", "recurring", "worsening", "improving"
	Confidence     float64 // 0.0-1.0
	Reasoning      string
	SuggestedTitle string
	PastFindings   []string // IDs of related Qdrant entries
}

// RoutingDecision determines where a finding is sent.
type RoutingDecision struct {
	Qdrant       bool
	GitHubIssue  bool
	WeeklyDigest bool
	IsDuplicate  bool
}
